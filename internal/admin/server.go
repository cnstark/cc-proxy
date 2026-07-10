package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cnstark/cc-proxy/internal/requestlog"
	"github.com/cnstark/cc-proxy/internal/web"
)

// ServerOption 配置 NewServer。
type ServerOption func(*Server)

func WithVersion(v string) ServerOption           { return func(s *Server) { s.version = v } }
func WithConfigPath(p string) ServerOption        { return func(s *Server) { s.configPath = p } }
func WithUsagePath(p string) ServerOption         { return func(s *Server) { s.usagePath = p } }
func WithReqLog(l *requestlog.Store) ServerOption { return func(s *Server) { s.reqLog = l } }

// NewServer 从 admin.json 加载密码与 session，组装 Server。
// admin.json 不存在或 enabled=false 时，后台对所有受保护路由返回 403。
func NewServer(adminPath string, opts ...ServerOption) (*Server, error) {
	s := &Server{adminPath: adminPath}
	for _, o := range opts {
		o(s)
	}
	ac, err := LoadAdmin(adminPath)
	if err != nil {
		// LoadAdmin 用 fmt.Errorf("%w") 包装 os.ReadFile 错误，os.IsNotExist 对这种
		// 再包装的 *fs.PathError 返回 false（Go 已知陷阱）。必须用 errors.Is 才能正确识别
		// "文件不存在" → 返回 disabled Server（403），而非 error。规格要求绝不裸奔。
		if errors.Is(err, os.ErrNotExist) {
			s.enabled = false
			return s, nil
		}
		return nil, fmt.Errorf("加载 admin.json 失败: %w", err)
	}
	if !ac.Enabled {
		s.enabled = false
		return s, nil
	}
	s.passwordHash = ac.PasswordHash
	s.sm = NewSessionManager(ac.SessionSecret)
	s.enabled = true
	return s, nil
}

// Mux 组装后台路由。
// 路由优先级（Go 1.22+ ServeMux）：更具体的模式优先。
//   - POST /api/login：公开，在顶层注册，比 /api/ 前缀更具体 → 不受 auth 保护。
//   - /api/：前缀，经 requireAuth 包装 authed 子 mux（含 /api/logout 等全部受保护路由）。
//   - GET /：子树，由静态资源 handler 处理；GET /api/* 会被 /api/ 前缀（更长）优先匹配。
func (s *Server) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleStatic)
	mux.HandleFunc("POST /api/login", s.handleLogin)

	authed := http.NewServeMux()
	authed.HandleFunc("POST /api/logout", s.handleLogout)
	authed.HandleFunc("GET /api/status", s.handleStatus)
	authed.HandleFunc("GET /api/config", s.handleConfigGet)
	authed.HandleFunc("PUT /api/config", s.handleConfigPut)
	authed.HandleFunc("POST /api/upstreams", s.handleUpstreamCreate)
	authed.HandleFunc("PUT /api/upstreams/{name}", s.handleUpstreamUpdate)
	authed.HandleFunc("DELETE /api/upstreams/{name}", s.handleUpstreamDelete)
	authed.HandleFunc("POST /api/projects", s.handleProjectCreate)
	authed.HandleFunc("PUT /api/projects/{name}", s.handleProjectUpdate)
	authed.HandleFunc("DELETE /api/projects/{name}", s.handleProjectDelete)
	authed.HandleFunc("POST /api/projects/{name}/direct-access", s.handleDirectAccess)
	authed.HandleFunc("POST /api/projects/{name}/mappings", s.handleMappingCreate)
	authed.HandleFunc("DELETE /api/projects/{name}/mappings/{model}", s.handleMappingDelete)
	authed.HandleFunc("POST /api/keys/gen", s.handleKeyGen)
	authed.HandleFunc("GET /api/logs", s.handleLogs)
	authed.HandleFunc("GET /api/logs/stream", s.handleLogsStream)
	authed.HandleFunc("GET /api/stats", s.handleStats)
	// Go 1.22+ ServeMux 不允许 method-specific (GET /) 与 method-agnostic (/api/)
	// 模式共存。注册所有方法前缀以消除冲突。
	mux.Handle("GET /api/", s.requireAuth(authed))
	mux.Handle("POST /api/", s.requireAuth(authed))
	mux.Handle("PUT /api/", s.requireAuth(authed))
	mux.Handle("DELETE /api/", s.requireAuth(authed))
	return mux
}

// handleStatic 提供 embed 前端静态资源。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	data, err := web.FS.ReadFile(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("content-type", webContentType(p))
	w.Write(data)
}

func webContentType(p string) string {
	switch filepath.Ext(p) {
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// Start 监听并服务，阻塞直到信号。
func (s *Server) Start(addr string, log *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: s.Mux()}
	errCh := make(chan error, 1)
	go func() {
		log.Info("admin 后台启动", "listen_addr", addr, "enabled", s.enabled)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	select {
	case err := <-errCh:
		return err
	case <-sigCh:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
