package admin

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnstark/cc-proxy/internal/requestlog"
	"github.com/cnstark/cc-proxy/internal/usage"
)

// Server 后台子服务器状态。
// reqLog 用具体类型 *requestlog.Store 而非接口，避免 nil 接口赋值陷阱
// （nil 的 *Store 赋给接口变量后接口非 nil，nil 判断失效）。
type Server struct {
	adminPath    string
	sm           *SessionManager
	passwordHash string
	enabled      bool

	configPath string
	usagePath  string
	version    string
	reqLog     *requestlog.Store // 可为 nil（创建失败或未启用）
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON encode failed: %v", err)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.enabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "后台未启用"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体格式错误"})
		return
	}
	if !VerifyPassword(body.Password, s.passwordHash) {
		writeJSON(w, 401, map[string]string{"error": "密码错误"})
		return
	}
	token, exp := s.sm.Issue()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  exp,
	})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"version":       s.version,
		"config_path":   s.configPath,
		"admin_enabled": s.enabled,
		"time":          time.Now().Format(time.RFC3339),
	})
}

// handleStats 返回用量 JSON。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	since := r.URL.Query().Get("since")
	if since == "" {
		since = "7d"
	}
	model := r.URL.Query().Get("model")
	f, err := usage.LoadFile(s.usagePath)
	if err != nil {
		writeJSON(w, 200, map[string]any{"rows": []any{}})
		return
	}
	sinceDate, _ := parseSince(since)
	rows := usage.Query(f, project, model, sinceDate)
	writeJSON(w, 200, map[string]any{"rows": rows})
}

// handleLogs 返回请求日志分页。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.reqLog == nil {
		writeJSON(w, 200, map[string]any{"rows": []any{}})
		return
	}
	rows := s.reqLog.Query(requestlog.QueryParams{
		Project: r.URL.Query().Get("project"),
		Limit:   100,
	})
	writeJSON(w, 200, map[string]any{"rows": rows})
}

// handleLogsStream SSE 推送新请求日志。
func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	if s.reqLog == nil {
		http.Error(w, `{"error":"请求日志未启用"}`, http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"不支持流式"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	project := r.URL.Query().Get("project")
	ch, cancel := s.reqLog.Subscribe(project)
	defer cancel()
	for {
		select {
		case row, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(row)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// parseSince 本地版（usage.parseSince 包内私有）。
func parseSince(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().AddDate(0, 0, -7).Format("2006-01-02"), nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return "", err
		}
		return time.Now().AddDate(0, 0, -n).Format("2006-01-02"), nil
	}
	return s, nil
}
