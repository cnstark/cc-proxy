package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	hash, _ := HashPassword("pw123")
	return &Server{
		adminPath:    filepath.Join(t.TempDir(), "admin.json"),
		sm:           NewSessionManager("test-secret-32-bytes-long-aaaaaa"),
		passwordHash: hash,
		enabled:      true,
	}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"pw123"}`))
	rr := httptest.NewRecorder()
	s.handleLogin(rr, req)
	if rr.Code != 200 {
		t.Fatalf("期望 200，得到 %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("set-cookie"), "ccp_admin=") {
		t.Errorf("应设置 cookie")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"wrong"}`))
	rr := httptest.NewRecorder()
	s.handleLogin(rr, req)
	if rr.Code != 401 {
		t.Errorf("期望 401，得到 %d", rr.Code)
	}
}

func TestRequireAuthRejectsNoCookie(t *testing.T) {
	s := newTestServer(t)
	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("不应进入受保护 handler")
	}))
	req := httptest.NewRequest("GET", "/api/config", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Errorf("期望 401，得到 %d", rr.Code)
	}
}

func TestRequireAuthAcceptsValidCookie(t *testing.T) {
	s := newTestServer(t)
	token, _ := s.sm.Issue()
	called := false
	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/api/config", nil)
	req.AddCookie(&http.Cookie{Name: "ccp_admin", Value: token})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !called {
		t.Errorf("有效 cookie 应放行")
	}
}
