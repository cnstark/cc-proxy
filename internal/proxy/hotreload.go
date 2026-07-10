package proxy

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cnstark/cc-proxy/internal/auth"
	"github.com/cnstark/cc-proxy/internal/circuitbreaker"
	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/project"
	"github.com/cnstark/cc-proxy/internal/requestlog"
	"github.com/cnstark/cc-proxy/internal/usage"
)

// ReloadingHandler 每次请求从 watcher 获取最新配置快照的热重载 handler。
// tracker 不在 snapshot 里（计数器是累积状态，不随配置热重载），挂在 handler 上每请求透传。
type ReloadingHandler struct {
	authStore *auth.Store
	forwarder Forwarder
	watcher   *config.Watcher
	tracker   usage.Recorder
	breaker   *circuitbreaker.Breaker
	reqLog    requestlog.Recorder
	log       *slog.Logger // 进程级 logger
}

// NewReloadingHandler 创建支持热重载的 handler。tracker 为进程级 usage 记录器。
func NewReloadingHandler(
	authStore *auth.Store,
	forwarder Forwarder,
	watcher *config.Watcher,
	tracker usage.Recorder,
	breaker *circuitbreaker.Breaker,
	reqLog requestlog.Recorder,
	log *slog.Logger,
) *ReloadingHandler {
	return &ReloadingHandler{
		authStore: authStore,
		forwarder: forwarder,
		watcher:   watcher,
		tracker:   tracker,
		breaker:   breaker,
		reqLog:    reqLog,
		log:       log,
	}
}

// ServeHTTP 每次请求从 watcher 重建 resolver/lookup
func (h *ReloadingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	snap := h.watcher.GetSnapshot()
	if snap == nil {
		writeError(w, http.StatusServiceUnavailable, "config_error", "配置未加载")
		return
	}

	// Rebuild auth keys on each request (allows key rotation via hot-reload)
	h.authStore.Update(snap.Server.PrivateKeys)

	// Build resolver from current snapshot
	routes := make(map[string]project.ProjectRoute, len(snap.Projects))
	for name, p := range snap.Projects {
		parsed := make(map[string][]project.ResolvedTarget, len(p.ModelMap))
		for alias, list := range p.ModelMap {
			targets := make([]project.ResolvedTarget, 0, len(list))
			for _, s := range list {
				if up, model, ok := config.ParseUpstreamModel(s); ok {
					targets = append(targets, project.ResolvedTarget{Upstream: up, Model: model})
				}
			}
			parsed[alias] = targets
		}
		routes[name] = project.ProjectRoute{
			ModelMap:    parsed,
			AllowDirect: p.AllowDirectAccess,
		}
	}
	resolver := project.NewResolver(routes, snap.ModelUpstreams)
	lookup := &snapshotLookup{snap: snap}

	// 生成 request_id：优先透传上游 x-request-id，fallback 自生成
	requestID := r.Header.Get("x-request-id")
	if requestID == "" {
		requestID = "ccp-" + generateShortID()
	}
	reqLogger := h.log.With("request_id", requestID)

	// 记录请求开始
	reqLogger.LogAttrs(r.Context(), slog.LevelDebug, "request started",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	)

	start := time.Now()
	defer func() {
		reqLogger.LogAttrs(r.Context(), slog.LevelDebug, "request completed",
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	}()

	// Build handler with current snapshot dependencies
	handler := &Handler{
		auth:              h.authStore,
		resolver:          resolver,
		lookup:            lookup,
		forwarder:         h.forwarder,
		log:               reqLogger,
		tracker:           h.tracker,
		usageEnabled:      snap.Server.UsageStats,
		breaker:           h.breaker,
		reqLog:            h.reqLog,
		requestLogEnabled: snap.Server.RequestLogEnabled != nil && *snap.Server.RequestLogEnabled,
		projectLogLevel: func(name string) config.LogLevel {
			if p, ok := snap.Projects[name]; ok {
				return p.LogLevel
			}
			return config.LogOff
		},
	}

	handler.ServeHTTP(w, r)
}

// generateShortID 生成 8 字符 base64 随机 ID。
// crypto/rand 几乎不会失败；失败时退化为时间戳 ID，保证唯一且非常量（request-id 非安全令牌）。
func generateShortID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}

// snapshotLookup implements ConfigLookup from snapshot
type snapshotLookup struct {
	snap *config.ConfigSnapshot
}

func (l *snapshotLookup) Upstream(name string) (config.Upstream, bool) {
	u, ok := l.snap.Upstreams[name]
	return u, ok
}
