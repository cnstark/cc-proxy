package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cnstark/cc-proxy/internal/requestlog"
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
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.enabled {
		http.Error(w, `{"error":"后台未启用"}`, http.StatusForbidden)
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
