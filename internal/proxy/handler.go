package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cnstark/cc-proxy/internal/circuitbreaker"
	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/logging"
	"github.com/cnstark/cc-proxy/internal/project"
	"github.com/cnstark/cc-proxy/internal/requestlog"
	"github.com/cnstark/cc-proxy/internal/usage"
)

// AuthStore 鉴权接口
type AuthStore interface {
	Authenticate(apiKey string) (projectName string, ok bool)
}

// ModelResolver 模型路由接口
type ModelResolver interface {
	Resolve(projectName, requestModel string) ([]project.ResolvedTarget, bool)
}

// ConfigLookup 配置查询接口
type ConfigLookup interface {
	Upstream(name string) (config.Upstream, bool)
}

// Forwarder 上游转发接口
type Forwarder interface {
	Forward(cfg config.Upstream, body []byte, headers http.Header, w http.ResponseWriter, c *usage.Collector, log *slog.Logger) error
}

// Handler 代理 HTTP handler
type Handler struct {
	auth              AuthStore
	resolver          ModelResolver
	lookup            ConfigLookup
	forwarder         Forwarder
	log               *slog.Logger
	tracker           usage.Recorder               // nil = usage 关闭
	usageEnabled      bool                         // 来自 per-request snapshot.Server.UsageStats
	breaker           *circuitbreaker.Breaker      // nil = 不启用熔断
	reqLog            requestlog.Recorder          // nil = 不记请求日志
	requestLogEnabled bool                         // 来自 snap.Server.RequestLogEnabled
	projectLogLevel   func(string) config.LogLevel // 查项目 log_level，决定是否记 body
}

// NewHandler 创建代理 handler
func NewHandler(auth AuthStore, resolver ModelResolver, lookup ConfigLookup, forwarder Forwarder, log *slog.Logger) *Handler {
	return &Handler{
		auth:      auth,
		resolver:  resolver,
		lookup:    lookup,
		forwarder: forwarder,
		log:       log,
	}
}

// ServeHTTP 处理请求：鉴权 → 解析模型 → 路由 → 转发
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// recover panic
	defer func() {
		if rec := recover(); rec != nil {
			h.log.InfoContext(r.Context(), "handler panic recovered", "panic", fmt.Sprintf("%v", rec))
			writeError(w, http.StatusInternalServerError, "internal_error", "内部服务错误")
		}
	}()

	// 请求日志：statusRecorder 捕获状态码，debug 级 tee 响应体。
	// w = sr 后，函数体内所有 writeError(w,...)/Forward(...,w,...)（含 recover 闭包）自动走 sr。
	sr := &statusRecorder{ResponseWriter: w}
	w = sr
	start := time.Now()
	var body []byte
	var projName, reqModelStr, hitUpstream, hitRealModel, reqErrStr string

	defer func() {
		if h.reqLog == nil || !h.requestLogEnabled {
			return
		}
		lvl := config.LogOff
		if h.projectLogLevel != nil {
			lvl = h.projectLogLevel(projName)
		}
		if lvl == config.LogOff {
			return
		}
		e := requestlog.Entry{
			Project:    projName,
			Method:     r.Method,
			Path:       r.URL.Path,
			Model:      reqModelStr,
			Upstream:   hitUpstream,
			RealModel:  hitRealModel,
			Status:     sr.status,
			DurationMs: time.Since(start).Milliseconds(),
			Error:      reqErrStr,
		}
		if lvl == config.LogDebug {
			e.RequestBody = truncStr(string(body), 65536)
			if sr.body != nil {
				e.ResponseBody = truncStr(sr.body.String(), 65536)
			}
		}
		h.reqLog.Record(e)
	}()

	// 1. 鉴权：优先 x-api-key，其次 Authorization: Bearer <key>
	apiKey := r.Header.Get("x-api-key")
	if apiKey == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			apiKey = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	projectName, ok := h.auth.Authenticate(apiKey)
	if !ok {
		h.log.InfoContext(r.Context(), "auth failed", "key_prefix", logging.MaskKey(apiKey))
		reqErrStr = "auth failed"
		writeError(w, http.StatusUnauthorized, "authentication_error", "无效的 API key")
		return
	}
	projName = projectName

	// 鉴权成功后附加 project 到 logger。
	// 安全前提：Handler 由 ReloadingHandler.ServeHTTP 每请求新建，不跨请求共享；
	// slog.With 返回新 logger 不修改原对象，此处重赋值不会引入数据竞争。
	h.log = h.log.With("project", projectName)

	// debug 级时初始化 body 缓冲，用于 tee 响应体
	if h.projectLogLevel != nil && h.projectLogLevel(projectName) == config.LogDebug {
		sr.body = &bytes.Buffer{}
	}

	// 2. 记录请求头（debug 级别，辅助排查上游兼容性问题）
	h.log.DebugContext(r.Context(), "request headers",
		"content_type", r.Header.Get("content-type"),
		"accept", r.Header.Get("accept"),
		"user_agent", r.Header.Get("user-agent"),
		"anthropic_ver", r.Header.Get("anthropic-version"),
		"content_length", r.Header.Get("content-length"),
	)

	// 3. 读取请求体
	b, err := io.ReadAll(r.Body)
	if err != nil {
		reqErrStr = "invalid request body"
		writeError(w, http.StatusBadRequest, "invalid_request_error", "无法读取请求体")
		return
	}
	body = b
	h.log.DebugContext(r.Context(), "request body received",
		"body_len", len(body),
		"content_type", r.Header.Get("content-type"),
		"raw_body_head", truncStr(string(body), 512),
	)

	// 4. 解析 model
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		h.log.DebugContext(r.Context(), "request body json parse failed",
			"error", err.Error(),
			"body_head", truncStr(string(body), 512),
			"body_len", len(body),
			"body_tail", truncTail(string(body), 128),
		)
		reqErrStr = "invalid request body"
		writeError(w, http.StatusBadRequest, "invalid_request_error", "请求体 JSON 解析失败")
		return
	}
	requestModel, ok := reqBody["model"].(string)
	if !ok || requestModel == "" {
		reqErrStr = "missing model field"
		writeError(w, http.StatusBadRequest, "invalid_request_error", "请求体缺少 model 字段")
		return
	}
	reqModelStr = requestModel

	// 5. 路由查表
	targets, ok := h.resolver.Resolve(projectName, requestModel)
	if !ok {
		h.log.InfoContext(r.Context(), "model not found",
			"model", requestModel,
		)
		reqErrStr = "model not found"
		writeError(w, http.StatusNotFound, "not_found_error",
			fmt.Sprintf("项目 %q 未配置模型 %q 的映射", projectName, requestModel))
		return
	}

	// 始终创建 collector：每个响应都解析 token 供 info 日志。
	// 落盘 usage.json 仅在 usage_stats 开启时（rec 非 nil）。
	var rec usage.Recorder
	if h.usageEnabled && h.tracker != nil {
		rec = h.tracker
	}
	collector := usage.NewCollector(rec, projectName, requestModel)

	// 6. 依次尝试转发
	allSkipped := true
	for _, target := range targets {
		cfg, ok := h.lookup.Upstream(target.Upstream)
		if !ok {
			continue
		}

		// 检查熔断器
		if h.breaker != nil {
			if avail, reason := h.breaker.IsAvailable(target.Upstream, cfg.RetryBackoff); !avail {
				h.log.InfoContext(r.Context(), "circuit breaker skipped",
					"upstream", target.Upstream,
					"reason", reason,
				)
				continue
			}
		}
		allSkipped = false

		// 故障转移时更新 usage collector 的 model 为该目标真实模型名
		if collector != nil {
			collector.SetModel(target.Model)
		}
		hitUpstream = target.Upstream
		hitRealModel = target.Model
		rewrittenBody, err := rewriteRequestBody(body, target.Model)
		if err != nil {
			h.log.InfoContext(r.Context(), "rewrite failed", "error", err.Error())
			reqErrStr = "request body rewrite failed"
			writeError(w, http.StatusInternalServerError, "internal_error", "请求重写失败")
			return
		}
		h.log.DebugContext(r.Context(), "forwarding to upstream",
			"upstream", target.Upstream,
			"upstream_url", cfg.URL,
			"rewritten_body_len", len(rewrittenBody),
			"rewritten_body_head", truncStr(string(rewrittenBody), 512),
			"rewritten_body_tail", truncTail(string(rewrittenBody), 128),
		)
		reqHeaders := r.Header.Clone()
		rewriteHeaders(reqHeaders, cfg.APIKey)

		fwdErr := h.forwarder.Forward(cfg, rewrittenBody, reqHeaders, w, collector, h.log)
		if fwdErr == nil {
			attrs := append([]any{"model", requestModel, "upstream", target.Upstream, "real_model", target.Model}, tokenAttrs(collector)...)
			h.log.InfoContext(r.Context(), "request forwarded", attrs...)
			// 记录成功
			if h.breaker != nil {
				if msg := h.breaker.RecordSuccess(target.Upstream); msg != "" {
					h.log.InfoContext(r.Context(), msg)
				}
			}
			return
		}
		// 不变量：响应已开始后（已 WriteHeader / 写了首字节）不得转移到下一个上游，
		// 否则两段响应拼接会让客户端收到截断/混乱的 JSON（unexpected end of JSON input）。
		// 此时也无法再向客户端写错误响应，只能记日志后终止。
		var startedErr *ResponseStartedError
		if errors.As(fwdErr, &startedErr) {
			h.log.InfoContext(r.Context(), "upstream failed after response started, aborting failover",
				"upstream", target.Upstream,
				"error", startedErr.Err.Error(),
			)
			reqErrStr = "upstream failed after response started"
			return
		}
		h.log.InfoContext(r.Context(), "upstream failed, trying next",
			"upstream", target.Upstream,
			"error", fwdErr.Error(),
		)

		// 记录失败（可重试错误）
		if h.breaker != nil {
			if msg := h.breaker.RecordFailure(target.Upstream, cfg.RetryBackoff); msg != "" {
				h.log.InfoContext(r.Context(), msg)
			}
		}
	}

	// 兜底逻辑：全部被跳过时强制探测第一个 target
	if allSkipped && h.breaker != nil && len(targets) > 0 {
		target := targets[0]
		cfg, ok := h.lookup.Upstream(target.Upstream)
		if ok {
			h.log.InfoContext(r.Context(), "all upstreams in backoff, forcing probe",
				"upstream", target.Upstream,
			)

			if collector != nil {
				collector.SetModel(target.Model)
			}
			hitUpstream = target.Upstream
			hitRealModel = target.Model
			rewrittenBody, err := rewriteRequestBody(body, target.Model)
			if err != nil {
				reqErrStr = "request body rewrite failed"
				writeError(w, http.StatusInternalServerError, "internal_error", "请求重写失败")
				return
			}
			reqHeaders := r.Header.Clone()
			rewriteHeaders(reqHeaders, cfg.APIKey)

			fwdErr := h.forwarder.Forward(cfg, rewrittenBody, reqHeaders, w, collector, h.log)
			if fwdErr == nil {
				attrs := append([]any{"model", requestModel, "upstream", target.Upstream, "real_model", target.Model}, tokenAttrs(collector)...)
				h.log.InfoContext(r.Context(), "forced probe succeeded", attrs...)
				if msg := h.breaker.RecordSuccess(target.Upstream); msg != "" {
					h.log.InfoContext(r.Context(), msg)
				}
				return
			}
			var startedErr *ResponseStartedError
			if errors.As(fwdErr, &startedErr) {
				h.log.InfoContext(r.Context(), "upstream failed after response started, aborting failover",
					"upstream", target.Upstream,
					"error", startedErr.Err.Error(),
				)
				reqErrStr = "upstream failed after response started"
				return
			}
			if msg := h.breaker.RecordFailure(target.Upstream, cfg.RetryBackoff); msg != "" {
				h.log.InfoContext(r.Context(), msg)
			}
		}
	}

	// 全部失败
	h.log.InfoContext(r.Context(), "all upstreams failed",
		"model", requestModel,
	)
	reqErrStr = "all upstreams failed"
	writeError(w, http.StatusBadGateway, "upstream_error", "所有上游均不可用")
}

// rewriteRequestBody 替换 JSON 请求体中的 model 字段。
// tokenAttrs 生成请求成功日志的 token 字段。解析到 usage 输出五类计数；否则输出 unknown 标记。
func tokenAttrs(c *usage.Collector) []any {
	if c == nil {
		return []any{"tokens", "unknown"}
	}
	u, saw := c.Stats()
	if !saw {
		return []any{"tokens", "unknown"}
	}
	return []any{
		"input", u.Input, "output", u.Output,
		"cache_creation", u.CacheCreation, "cache_read", u.CacheRead,
		"total", u.Input + u.Output + u.CacheCreation + u.CacheRead,
	}
}

// 使用 json.Encoder + SetEscapeHTML(false)，避免把请求体里的 <、>、& 等
// HTML 特殊字符转义成 < 等——这些字符在 Claude Code 的 system-reminder
// 和工具描述里很常见，转义虽 JSON 语义等价，但会改变字节内容并可能触发某些
// 上游解析器的边界问题，原样保留更安全。
func rewriteRequestBody(body []byte, targetModel string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("rewrite: json 解析失败: %w", err)
	}
	m["model"] = targetModel
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("rewrite: json 序列化失败: %w", err)
	}
	// json.Encoder.Encode 会追加一个换行符，去掉以保持与原 body 一致
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// rewriteHeaders 删除原认证头，写入上游 key
func rewriteHeaders(h http.Header, upstreamKey string) {
	h.Del("x-api-key")
	h.Del("Authorization")
	h.Set("x-api-key", upstreamKey)
}

func writeError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	})
}

// truncStr 截取字符串前 n 个字符用于日志，避免落盘过大
func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// truncTail 截取字符串末尾 n 个字符，便于观察 JSON 是否被截断
func truncTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(truncated)..." + s[len(s)-n:]
}

// statusRecorder 包装 ResponseWriter，捕获状态码并在 debug 级 tee 响应体。
// 必须实现 http.Flusher 并委托给底层，否则流式 SSE 透传会失效
// （forward.go 的 http.Flusher 依赖：flusher, canFlush := w.(http.Flusher)）。
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer // 非 nil 时 tee 响应体（仅 debug 级）
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = 200 // 隐式 200
	}
	if s.body != nil {
		s.body.Write(b) // tee，不阻塞客户端
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
