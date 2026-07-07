package proxy

import (
	"encoding/json"
	"github.com/cnstark/claude-switch/internal/auth"
	"github.com/cnstark/claude-switch/internal/config"
	"github.com/cnstark/claude-switch/internal/logging"
	"github.com/cnstark/claude-switch/internal/project"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntegration_FullFlow(t *testing.T) {
	// Fake upstream: verifies rewritten model and correct API key
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-real-upstream" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		if m["model"] != "claude-opus-4-8" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"unexpected model"}`))
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_123",
			"model": "claude-opus-4-8",
			"content": []map[string]any{
				{"type": "text", "text": "Hello from upstream"},
			},
		})
	}))
	defer ts1.Close()

	cfgUpstreams := map[string]config.Upstream{
		"cfg1": {
			Name: "cfg1", URL: ts1.URL, APIKey: "sk-real-upstream",
			Models: []string{"claude-opus-4-8"}, Timeout: 5 * time.Second,
		},
	}
	keys := map[string]string{"sk-cs-myproject": "myproject"}
	projMap := map[string]map[string][]string{
		"myproject": {"my-model-alias": {"cfg1"}},
	}

	authStore := auth.NewStore(keys)
	resolver := newAliasResolver(projMap)
	lookup := &configLookup{upstreams: cfgUpstreams}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()

	handler := NewHandler(authStore, resolver, lookup, fwd, log)

	reqBody := `{"model":"my-model-alias","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("x-api-key", "sk-cs-myproject")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["id"] != "msg_123" {
		t.Fatalf("expected response from upstream, got %v", resp)
	}
	if rec.Header().Get("content-type") != "application/json" {
		t.Fatalf("expected content-type application/json, got %s", rec.Header().Get("content-type"))
	}
}

func TestIntegration_StreamingFullFlow(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		for _, chunk := range []string{"Hello", " ", "World"} {
			w.Write([]byte("data: " + chunk + "\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	cfgUpstreams := map[string]config.Upstream{
		"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k1", Models: []string{"real-m"}, Timeout: 5 * time.Second},
	}
	keys := map[string]string{"sk-cs-p1": "p1"}
	projMap := map[string]map[string][]string{"p1": {"m": {"cfg1"}}}

	handler := NewHandler(
		auth.NewStore(keys),
		newAliasResolver(projMap),
		&configLookup{upstreams: cfgUpstreams},
		NewStreamingForwarder(),
		logging.NewNopLogger(),
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","stream":true}`))
	req.Header.Set("x-api-key", "sk-cs-p1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hello") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected streaming content, got: %s", body)
	}
}

func TestIntegration_DirectAccess_FullFlow(t *testing.T) {
	// Fake upstream: 校验直连时 body 的 model 被改写为真实 Model
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-real-upstream" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		if m["model"] != "claude-opus-4-8" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"unexpected model"}`))
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_direct_integration",
			"model": "claude-opus-4-8",
			"content": []map[string]any{
				{"type": "text", "text": "Hello from direct upstream"},
			},
		})
	}))
	defer ts1.Close()

	cfgUpstreams := map[string]config.Upstream{
		"cfg1": {
			Name: "cfg1", URL: ts1.URL, APIKey: "sk-real-upstream",
			Models: []string{"claude-opus-4-8"}, Timeout: 5 * time.Second,
		},
	}
	keys := map[string]string{"sk-cs-myproject": "myproject"}
	routes := map[string]project.ProjectRoute{
		"myproject": {
			AllowDirect: true,
			ModelMap:    map[string][]string{"my-model-alias": {"cfg1"}},
		},
	}
	upstreamNames := map[string]bool{"cfg1": true}

	authStore := auth.NewStore(keys)
	resolver := project.NewResolver(routes, upstreamNames)
	lookup := &configLookup{upstreams: cfgUpstreams}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	handler := NewHandler(authStore, resolver, lookup, fwd, log)

	// 请求 model 直接用 cfg name "cfg1"
	reqBody := `{"model":"cfg1","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("x-api-key", "sk-cs-myproject")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["id"] != "msg_direct_integration" {
		t.Fatalf("expected response from direct upstream, got %v", resp)
	}
}

func TestIntegration_DirectAccess_HotReloadDisable(t *testing.T) {
	// 模拟热重载：同一项目从 AllowDirect=true 切到 false 后，直连请求应返回 404。
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{"id": "ok", "model": "claude-opus-4-8"})
	}))
	defer ts1.Close()

	cfgUpstreams := map[string]config.Upstream{
		"cfg1": {Name: "cfg1", URL: ts1.URL, APIKey: "k", Models: []string{"claude-opus-4-8"}, Timeout: 5 * time.Second},
	}
	upstreamNames := map[string]bool{"cfg1": true}
	keys := map[string]string{"sk-cs-myproject": "myproject"}
	authStore := auth.NewStore(keys)
	lookup := &configLookup{upstreams: cfgUpstreams}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()

	// 初始：开启直连
	routesOn := map[string]project.ProjectRoute{
		"myproject": {AllowDirect: true, ModelMap: map[string][]string{}},
	}
	handlerOn := NewHandler(authStore, project.NewResolver(routesOn, upstreamNames), lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"cfg1"}`))
	req.Header.Set("x-api-key", "sk-cs-myproject")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	handlerOn.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 with direct access on, got %d: %s", rec.Code, rec.Body.String())
	}

	// 热重载后：关闭直连
	routesOff := map[string]project.ProjectRoute{
		"myproject": {AllowDirect: false, ModelMap: map[string][]string{}},
	}
	handlerOff := NewHandler(authStore, project.NewResolver(routesOff, upstreamNames), lookup, fwd, log)

	req2 := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"cfg1"}`))
	req2.Header.Set("x-api-key", "sk-cs-myproject")
	req2.Header.Set("content-type", "application/json")
	rec2 := httptest.NewRecorder()
	handlerOff.ServeHTTP(rec2, req2)
	if rec2.Code != 404 {
		t.Fatalf("expected 404 after direct access disabled, got %d", rec2.Code)
	}
}
