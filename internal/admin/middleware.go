package admin

import "net/http"

const cookieName = "ccp_admin"

// requireAuth 校验 session cookie，失败返回 401；后台未启用返回 403。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.enabled {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "后台未启用，请先 ccp admin set-password"})
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || !s.sm.Verify(c.Value) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话过期"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
