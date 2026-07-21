package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnstark/cc-proxy/internal/auth"
	"github.com/cnstark/cc-proxy/internal/circuitbreaker"
	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/logging"
	"github.com/cnstark/cc-proxy/internal/project"
	"github.com/cnstark/cc-proxy/internal/requestlog"
	"github.com/cnstark/cc-proxy/internal/upstream"
	"github.com/cnstark/cc-proxy/internal/usage"
)

// configLookup implements ConfigLookup interface for tests
type configLookup struct {
	upstreams map[string]config.Upstream
}

func (c *configLookup) Upstream(name string) (config.Upstream, bool) {
	u, ok := c.upstreams[name]
	return u, ok
}

// newAliasResolver 从「项目 → 别名表」结构构造无直连的 resolver。
// 输入为 raw 字符串（upstream/model），内部预解析为 ResolvedTarget（与 hotreload 一致）。
// AllowDirect=false，modelUpstreams=nil。
func newAliasResolver(projMap map[string]map[string][]string) *project.ModelResolver {
	routes := make(map[string]project.ProjectRoute, len(projMap))
	for name, mm := range projMap {
		parsed := make(map[string][]project.ResolvedTarget, len(mm))
		for alias, list := range mm {
			targets := make([]project.ResolvedTarget, 0, len(list))
			for _, s := range list {
				if up, model, ok := config.ParseUpstreamModel(s); ok {
					targets = append(targets, project.ResolvedTarget{Upstream: up, Model: model})
				}
			}
			parsed[alias] = targets
		}
		routes[name] = project.ProjectRoute{ModelMap: parsed}
	}
	return project.NewResolver(routes, nil)
}

func setupTestHandler(keys map[string]string, projMap map[string]map[string][]string, upstreams map[string]config.Upstream) *Handler {
	authStore := auth.NewStore(keys)
	resolver := newAliasResolver(projMap)
	lookup := &configLookup{upstreams: upstreams}
	fwd := upstream.NewClient()
	log := logging.NewNopLogger()
	return NewHandler(authStore, resolver, lookup, fwd, log)
}

// captureLogger 返回写入 buf 的 TextHandler logger（Info 级别），供断言请求日志字段。
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func TestHandler_AuthFailure_401(t *testing.T) {
	h := setupTestHandler(
		map[string]string{"sk-cp-key1": "p1"},
		nil,
		nil,
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "bad-key")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	var errResp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp["type"] != "error" {
		t.Fatal("expected Anthropic error format")
	}
}

func TestHandler_UnknownModel_404(t *testing.T) {
	h := setupTestHandler(
		map[string]string{"sk-cp-key1": "p1"},
		map[string]map[string][]string{
			"p1": {"knownModel": {"cfg1/real-m"}},
		},
		map[string]config.Upstream{
			"cfg1": {Name: "cfg1", URL: "http://example.com", APIKey: "k", Models: []string{"real-m"}, Timeout: 0},
		},
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"unknownModel"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_MissingAPIKey_401(t *testing.T) {
	h := setupTestHandler(
		map[string]string{"sk-cp-key1": "p1"},
		nil,
		nil,
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("expected 401 for missing api key, got %d", rec.Code)
	}
}

func TestHandler_BearerTokenAuth_Success(t *testing.T) {
	h := setupTestHandler(
		map[string]string{"sk-cp-key1": "p1"},
		map[string]map[string][]string{
			"p1": {"m": {"cfg1/real-m"}},
		},
		map[string]config.Upstream{
			"cfg1": {Name: "cfg1", URL: "http://127.0.0.1:1", APIKey: "k", Models: []string{"real-m"}, Timeout: 0},
		},
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// 不再是 401 即表示 Bearer token 鉴权通过
	if rec.Code == 401 {
		t.Fatal("expected Bearer token auth to succeed, got 401")
	}
}

func TestHandler_BearerTokenAuth_Failure(t *testing.T) {
	h := setupTestHandler(
		map[string]string{"sk-cp-key1": "p1"},
		nil,
		nil,
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer bad-key")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("expected 401 for bad Bearer token, got %d", rec.Code)
	}
}

func TestHandler_XAPIKeyTakesPrecedence(t *testing.T) {
	h := setupTestHandler(
		map[string]string{"sk-cp-key1": "p1"},
		map[string]map[string][]string{
			"p1": {"m": {"cfg1/real-m"}},
		},
		map[string]config.Upstream{
			"cfg1": {Name: "cfg1", URL: "http://127.0.0.1:1", APIKey: "k", Models: []string{"real-m"}, Timeout: 0},
		},
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("Authorization", "Bearer bad-key") // x-api-key 应优先
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// x-api-key 优先，正确的 key 应该通过鉴权
	if rec.Code == 401 {
		t.Fatal("expected x-api-key to take precedence over Authorization header, got 401")
	}
}

func TestHandler_Failover_CountsOnce(t *testing.T) {
	// cfg1 连接失败 → cfg2 成功带 usage → 只计一次
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":42,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"))
		flusher.Flush()
	}))
	defer ts2.Close()

	cfg1 := config.Upstream{Name: "cfg1", URL: "http://127.0.0.1:19996", APIKey: "k1", Models: []string{"m1"}, Timeout: 50 * time.Millisecond}
	cfg2 := config.Upstream{Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}

	rec := &usageFakeRecorder{}
	h := &Handler{
		auth:         auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:     newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}}),
		lookup:       &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}},
		forwarder:    NewStreamingForwarder(),
		log:          logging.NewNopLogger(),
		tracker:      rec,
		usageEnabled: true,
	}

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200 via cfg2, got %d", w.Code)
	}
	if rec.calls != 1 {
		t.Fatalf("expected exactly 1 usage commit, got %d", rec.calls)
	}
	if rec.u.Input != 42 || rec.u.Output != 7 {
		t.Fatalf("unexpected usage: %+v", rec.u)
	}
	// 故障转移后 model 应为上游真实模型名（cfg2.Models[0]="m2"），而非 model_map 的 key（"m"）
	if rec.model != "m2" {
		t.Fatalf("expected model recorded as upstream real model 'm2', got %q", rec.model)
	}
}

func TestHandler_ErrorResponsePassthrough_NoCount(t *testing.T) {
	// 上游返回 400 错误（不可重试）→ 透传，不计数
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
	}))
	defer ts.Close()

	rec := &usageFakeRecorder{}
	h := &Handler{
		auth:         auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:     newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m"}}}),
		lookup:       &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"m"}, Timeout: 5 * time.Second}}},
		forwarder:    NewStreamingForwarder(),
		log:          logging.NewNopLogger(),
		tracker:      rec,
		usageEnabled: true,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 passthrough, got %d", w.Code)
	}
	if rec.calls != 0 {
		t.Fatalf("expected no usage for error response, got %d", rec.calls)
	}
}

func TestIntegration_UsagePersisted_ReadBackByStats(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	usagePath := filepath.Join(dir, "usage.json")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":125,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":25}}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	cfgYAML := fmt.Sprintf(`
server:
  listen: 127.0.0.1:8787
  usage_stats: true
  private_keys:
    sk-cp-key1: p1
upstreams:
  - name: cfg1
    url: %s
    apikey: k
    models: [real]
    timeout: 5s
projects:
  - name: p1
    log_level: off
    model_map:
      m: [cfg1/real]
`, ts.URL)
	os.WriteFile(configPath, []byte(cfgYAML), 0600)

	watcher := config.NewWatcher(configPath, 50*time.Millisecond, logging.NewNopLogger())
	defer watcher.Stop()
	tracker := usage.NewTracker(usagePath)
	defer tracker.Close()

	snap, err := watcher.Current()
	if err != nil {
		t.Fatalf("watcher current: %v", err)
	}
	authStore := auth.NewStore(snap.Server.PrivateKeys)
	fwd := NewStreamingForwarder()
	handler := NewReloadingHandler(authStore, fwd, watcher, tracker, nil, nil, logging.NewNopLogger())

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	tracker.Flush()

	out, err := usage.RunStats(usagePath, "p1", "1970-01-01", "")
	if err != nil {
		t.Fatalf("runstats: %v", err)
	}
	if !strings.Contains(out, "125") || !strings.Contains(out, "25") {
		t.Fatalf("expected persisted usage in stats output, got: %s", out)
	}
	// 统计 key 应为上游真实模型名 "real"，而非 model_map 的 key "m"
	if !strings.Contains(out, "real") {
		t.Fatalf("expected stats to use upstream real model 'real', got: %s", out)
	}
}

func TestHandler_MissingModelField_400(t *testing.T) {
	h := setupTestHandler(
		map[string]string{"sk-cp-key1": "p1"},
		map[string]map[string][]string{"p1": {"m": {"cfg1/real-m"}}},
		map[string]config.Upstream{
			"cfg1": {Name: "cfg1", URL: "http://example.com", APIKey: "k", Models: []string{"real-m"}, Timeout: 0},
		},
	)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"max_tokens":100}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for missing model field, got %d", rec.Code)
	}
}

// ── Circuit breaker integration tests ──

// TestBreaker_BackoffSkipsUpstream 验证退避期内跳过上游，故障转移到备选
func TestBreaker_BackoffSkipsUpstream(t *testing.T) {
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok from backup"}`))
	}))
	defer ts2.Close()

	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(503)
		w.Write([]byte(`{"type":"error","error":{"type":"overloaded"}}`))
	}))
	defer ts1.Close()

	cfg1 := config.Upstream{
		Name: "cfg1", URL: ts1.URL, APIKey: "k1", Models: []string{"m1"},
		Timeout: 5 * time.Second, RetryBackoff: []time.Duration{10 * time.Minute},
	}
	cfg2 := config.Upstream{
		Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"},
		Timeout: 5 * time.Second,
	}

	breaker := circuitbreaker.NewBreaker()

	h := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}},
		forwarder: NewStreamingForwarder(),
		log:       logging.NewNopLogger(),
		breaker:   breaker,
	}

	// 前两次请求：cfg1 返回 503，故障转移到 cfg2 成功；cfg1 累积 2 次失败进入退避
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("x-api-key", "sk-cp-key1")
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("request %d: expected 200 via cfg2, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	// 第三次请求：cfg1 在退避期内，直接被跳过，只用 cfg2
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 via cfg2 (cfg1 in backoff), got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBreaker_SingleUpstream_ForcesProbe 验证单 upstream 全部被跳过时兜底探测
func TestBreaker_SingleUpstream_ForcesProbe(t *testing.T) {
	ts503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(503)
		w.Write([]byte(`{"type":"error","error":{"type":"overloaded"}}`))
	}))
	defer ts503.Close()

	ts200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts200.Close()

	breaker := circuitbreaker.NewBreaker()

	cfg503 := config.Upstream{
		Name: "cfg1", URL: ts503.URL, APIKey: "k1", Models: []string{"m1"},
		Timeout: 5 * time.Second, RetryBackoff: []time.Duration{10 * time.Minute},
	}

	h503 := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg503}},
		forwarder: NewStreamingForwarder(),
		log:       logging.NewNopLogger(),
		breaker:   breaker,
	}

	// 两次 503 触发退避（每个请求 cfg1 返回可重试错误，最终 502）
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("x-api-key", "sk-cp-key1")
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		h503.ServeHTTP(rec, req)
		if rec.Code != 502 {
			t.Fatalf("request %d: expected 502 (only one upstream in backoff), got %d", i+1, rec.Code)
		}
	}

	// 第三次请求：cfg1 在退避期内，但单 upstream 触发兜底强制探测
	// 换回正常的 upstream（返回 200）验证强制探测能成功
	cfg200 := config.Upstream{
		Name: "cfg1", URL: ts200.URL, APIKey: "k1", Models: []string{"m1"},
		Timeout: 5 * time.Second, RetryBackoff: []time.Duration{10 * time.Minute},
	}
	h := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg200}},
		forwarder: NewStreamingForwarder(),
		log:       logging.NewNopLogger(),
		breaker:   breaker,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected forced probe to succeed with 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBreaker_NoBackoffUpstream_NotAffected 验证无 backoff 的 upstream 不受影响
func TestBreaker_NoBackoffUpstream_NotAffected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	cfg1 := config.Upstream{
		Name: "cfg1", URL: ts.URL, APIKey: "k1", Models: []string{"m1"},
		Timeout: 5 * time.Second, // 无 RetryBackoff
	}

	breaker := circuitbreaker.NewBreaker()

	h := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1}},
		forwarder: NewStreamingForwarder(),
		log:       logging.NewNopLogger(),
		breaker:   breaker,
	}

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 for upstream without backoff, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBreaker_4xxNotCounted 验证不可重试的 4xx 不计入熔断
func TestBreaker_4xxNotCounted(t *testing.T) {
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts2.Close()

	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error"}}`))
	}))
	defer ts1.Close()

	cfg1 := config.Upstream{
		Name: "cfg1", URL: ts1.URL, APIKey: "k1", Models: []string{"m1"},
		Timeout: 5 * time.Second, RetryBackoff: []time.Duration{10 * time.Minute},
	}
	cfg2 := config.Upstream{
		Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"},
		Timeout: 5 * time.Second, RetryBackoff: []time.Duration{10 * time.Minute},
	}

	breaker := circuitbreaker.NewBreaker()

	h := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}},
		forwarder: NewStreamingForwarder(),
		log:       logging.NewNopLogger(),
		breaker:   breaker,
	}

	// 多次 400（不可重试），不应触发 cfg1 的熔断
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("x-api-key", "sk-cp-key1")
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		// 400 应直接透传，不故障转移到 cfg2
		if rec.Code != 400 {
			t.Fatalf("request %d: expected 400 passthrough, got %d", i+1, rec.Code)
		}
	}

	// 确认 cfg1 未被熔断：换一个返回 200 的 upstream 验证
	ts200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts200.Close()

	cfg1OK := config.Upstream{
		Name: "cfg1", URL: ts200.URL, APIKey: "k1", Models: []string{"m1"},
		Timeout: 5 * time.Second, RetryBackoff: []time.Duration{10 * time.Minute},
	}
	h.lookup = &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1OK, "cfg2": cfg2}}

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("cfg1 should not be in backoff (4xx not counted), expected 200, got %d", rec.Code)
	}
}

// TestHandler_Forwarded_NoDuplicateProjectField 验证成功转发日志中 project 字段只出现一次。
// h.log 在鉴权后已通过 slog.With("project",...) 挂载 project，日志调用不应再重复传 project attr，
// 否则 slog 不去重会输出两个 project 字段。
func TestHandler_Forwarded_NoDuplicateProjectField(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	var buf bytes.Buffer
	h := &Handler{
		auth:         auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:     newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m"}}}),
		lookup:       &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"m"}, Timeout: 5 * time.Second}}},
		forwarder:    NewStreamingForwarder(),
		log:          captureLogger(&buf),
		usageEnabled: false,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 找到 "request forwarded" 那一行，断言其中 project= 只出现一次
	var forwardedLine string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "request forwarded") {
			forwardedLine = line
			break
		}
	}
	if forwardedLine == "" {
		t.Fatalf("未找到 request forwarded 日志行:\n%s", buf.String())
	}
	if got := strings.Count(forwardedLine, "project="); got != 1 {
		t.Fatalf("expected exactly 1 project= in forwarded log line, got %d:\n%s", got, forwardedLine)
	}
}

func TestHandler_Forwarded_LogsTokenFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12500,\"cache_creation_input_tokens\":800,\"cache_read_input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3200}}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	rec := &usageFakeRecorder{}
	var buf bytes.Buffer
	h := &Handler{
		auth:         auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:     newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m"}}}),
		lookup:       &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"m"}, Timeout: 5 * time.Second}}},
		forwarder:    NewStreamingForwarder(),
		log:          captureLogger(&buf),
		tracker:      rec,
		usageEnabled: true,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if rec.calls != 1 {
		t.Fatalf("expected 1 persist call (usage_stats on), got %d", rec.calls)
	}
	out := buf.String()
	for _, want := range []string{"input=12500", "output=3200", "cache_creation=800", "cache_read=0", "total=16500"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
}

func TestHandler_UsageDisabled_LogsTokensWithoutPersisting(t *testing.T) {
	// usage_stats 关：仍解析供日志（tokens 入日志），但不落盘（Record 不被调）。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":42,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	rec := &usageFakeRecorder{}
	var buf bytes.Buffer
	h := &Handler{
		auth:         auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:     newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m"}}}),
		lookup:       &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"m"}, Timeout: 5 * time.Second}}},
		forwarder:    NewStreamingForwarder(),
		log:          captureLogger(&buf),
		tracker:      rec,
		usageEnabled: false,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if rec.calls != 0 {
		t.Fatalf("expected no persist when usage_stats off, got %d", rec.calls)
	}
	out := buf.String()
	for _, want := range []string{"input=42", "output=7", "total=49"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q (tokens should be logged even when usage_stats off):\n%s", want, out)
		}
	}
}

func TestHandler_NoUsage_LogsUnknownToken(t *testing.T) {
	// 非 Anthropic 风格 2xx 响应无 usage → 日志应输出 tokens=unknown，且不落盘。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	rec := &usageFakeRecorder{}
	var buf bytes.Buffer
	h := &Handler{
		auth:         auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:     newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m"}}}),
		lookup:       &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"m"}, Timeout: 5 * time.Second}}},
		forwarder:    NewStreamingForwarder(),
		log:          captureLogger(&buf),
		tracker:      rec,
		usageEnabled: true,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "tokens=unknown") {
		t.Fatalf("expected tokens=unknown in log, got:\n%s", out)
	}
	if rec.calls != 0 {
		t.Fatalf("expected no persist for usage-less response, got %d", rec.calls)
	}
}

func TestHandler_DirectAccess_ForwardsToUpstream(t *testing.T) {
	// 直接访问：请求 model = 真实模型名 "real-m"，应转发到 cfg1 并把 body model 改写为 "real-m"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "upstream-key" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		if m["model"] != "real-m" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"unexpected model"}`))
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{"id": "msg_direct", "model": "real-m"})
	}))
	defer ts.Close()

	upstreams := map[string]config.Upstream{
		"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "upstream-key", Models: []string{"real-m"}, Timeout: 5 * time.Second},
	}
	routes := map[string]project.ProjectRoute{
		"p1": {AllowDirect: true, ModelMap: map[string][]project.ResolvedTarget{"aliasA": {{Upstream: "cfg1", Model: "real-m"}}}},
	}
	modelUpstreams := map[string][]string{"real-m": {"cfg1"}}
	resolver := project.NewResolver(routes, modelUpstreams)

	authStore := auth.NewStore(map[string]string{"sk-cp-key1": "p1"})
	lookup := &configLookup{upstreams: upstreams}
	fwd := upstream.NewClient()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"real-m","max_tokens":100}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["id"] != "msg_direct" {
		t.Fatalf("expected response from direct upstream, got %v", resp)
	}
}

func TestHandler_DirectAccess_Disabled_Returns404(t *testing.T) {
	upstreams := map[string]config.Upstream{
		"cfg1": {Name: "cfg1", URL: "http://example.com", APIKey: "k", Models: []string{"real-m"}, Timeout: 0},
	}
	routes := map[string]project.ProjectRoute{
		"p1": {AllowDirect: false, ModelMap: map[string][]project.ResolvedTarget{"aliasA": {{Upstream: "cfg1", Model: "real-m"}}}},
	}
	resolver := project.NewResolver(routes, nil)

	authStore := auth.NewStore(map[string]string{"sk-cp-key1": "p1"})
	lookup := &configLookup{upstreams: upstreams}
	fwd := upstream.NewClient()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"real-m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404 when direct access disabled, got %d", rec.Code)
	}
}

// ── ResponseStartedError 不变量测试 ──

// startedErrForwarder 写入部分响应后返回 ResponseStartedError，模拟流式响应中途失败。
type startedErrForwarder struct {
	written []byte
}

func (f *startedErrForwarder) Forward(cfg config.Upstream, body []byte, headers http.Header, w http.ResponseWriter, c *usage.Collector, log *slog.Logger) error {
	w.WriteHeader(200)
	w.Write(f.written)
	return &ResponseStartedError{Err: fmt.Errorf("simulated mid-stream write failure")}
}

// recordingForwarder 记录是否被调用。
type recordingForwarder struct {
	hit    *bool
	status int
	body   []byte
}

func (f *recordingForwarder) Forward(cfg config.Upstream, body []byte, headers http.Header, w http.ResponseWriter, c *usage.Collector, log *slog.Logger) error {
	*f.hit = true
	w.WriteHeader(f.status)
	w.Write(f.body)
	return nil
}

// dispatchForwarder 按 upstream 名分派到不同的 Forwarder。
type dispatchForwarder struct {
	byUpstream map[string]Forwarder
}

func (d *dispatchForwarder) Forward(cfg config.Upstream, body []byte, headers http.Header, w http.ResponseWriter, c *usage.Collector, log *slog.Logger) error {
	if f, ok := d.byUpstream[cfg.Name]; ok {
		return f.Forward(cfg, body, headers, w, c, log)
	}
	return fmt.Errorf("no fake forwarder for upstream %q", cfg.Name)
}

// TestForcedProbe_ResponseStarted_No502 验证 forced-probe 兜底分支遵守流已开始不变量：
// 当 probe 的 Forward 在响应已开始后失败（ResponseStartedError），handler 不得写 502
// （否则会将错误 JSON 拼接到已开始的流上），也不应落入最终的 writeError。直接 return。
func TestForcedProbe_ResponseStarted_No502(t *testing.T) {
	fwd := &startedErrForwarder{written: []byte("data: partial")}

	cfg1 := config.Upstream{
		Name: "cfg1", URL: "http://127.0.0.1:1", APIKey: "k1", Models: []string{"m1"},
		Timeout: 5 * time.Second, RetryBackoff: []time.Duration{10 * time.Minute},
	}
	breaker := circuitbreaker.NewBreaker()

	// 先制造两次失败让 cfg1 进入退避
	hSetup := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1}},
		forwarder: NewStreamingForwarder(),
		log:       logging.NewNopLogger(),
		breaker:   breaker,
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("x-api-key", "sk-cp-key1")
		rec := httptest.NewRecorder()
		hSetup.ServeHTTP(rec, req)
		if rec.Code != 502 {
			t.Fatalf("setup req %d: expected 502, got %d", i+1, rec.Code)
		}
	}

	// 现在 cfg1 在退避期。用 fake forwarder 发 forced-probe，返回 ResponseStartedError
	h := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1}},
		forwarder: fwd,
		log:       logging.NewNopLogger(),
		breaker:   breaker,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == 502 {
		t.Fatalf("forced-probe must not write 502 after ResponseStartedError; got 502, body=%q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: partial") {
		t.Fatalf("expected the partial streamed response to be preserved, got body=%q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "upstream_error") || strings.Contains(rec.Body.String(), "所有上游均不可用") {
		t.Fatalf("forced-probe spliced a 502 error body onto the started stream: %q", rec.Body.String())
	}
}

// TestHandler_MainLoop_ResponseStartedError_NoFailover 验证主循环在 ResponseStartedError 时不转移、不写 502。
func TestHandler_MainLoop_ResponseStartedError_NoFailover(t *testing.T) {
	cfg1 := config.Upstream{Name: "cfg1", URL: "http://example.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 5 * time.Second}
	cfg2 := config.Upstream{Name: "cfg2", URL: "http://example.com", APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}

	probeHit := false
	fwdCfg2 := &recordingForwarder{hit: &probeHit, status: 200, body: []byte(`{"ok":true}`)}

	h := &Handler{
		auth:     auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver: newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}}),
		lookup:   &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}},
		forwarder: &dispatchForwarder{
			byUpstream: map[string]Forwarder{
				"cfg1": &startedErrForwarder{written: []byte("data: partial")},
				"cfg2": fwdCfg2,
			},
		},
		log: logging.NewNopLogger(),
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if probeHit {
		t.Fatalf("main loop must NOT failover after ResponseStartedError, but cfg2 was probed")
	}
	if rec.Code == 502 {
		t.Fatalf("main loop must not write 502 after ResponseStartedError; body=%q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data: partial") {
		t.Fatalf("expected partial response preserved, got %q", rec.Body.String())
	}
}

// ── statusRecorder tests ──

func TestStatusRecorderCapturesStatusAndFlushes(t *testing.T) {
	rr := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rr}
	sr.WriteHeader(404)
	if sr.status != 404 {
		t.Errorf("状态未捕获: %d", sr.status)
	}
	sr.body = &bytes.Buffer{}
	sr.Write([]byte("hello"))
	if sr.body.String() != "hello" {
		t.Errorf("body 未 tee: %q", sr.body.String())
	}
	if _, ok := interface{}(sr).(http.Flusher); !ok {
		t.Errorf("statusRecorder 必须实现 http.Flusher（否则流式失效）")
	}
	sr.Flush() // 不应 panic
}

func TestStatusRecorderImplicit200(t *testing.T) {
	rr := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rr}
	sr.Write([]byte("x"))
	if sr.status != 200 {
		t.Errorf("隐式状态应为 200，得到 %d", sr.status)
	}
}

// ── request log integration tests ──

// fakeReqLog 收集 Record 调用，供断言。实现 requestlog.Recorder。
type fakeReqLog struct {
	mu      sync.Mutex
	entries []requestlog.Entry
}

func (f *fakeReqLog) Record(e requestlog.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
}

func (f *fakeReqLog) snapshot() []requestlog.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]requestlog.Entry, len(f.entries))
	copy(cp, f.entries)
	return cp
}

func TestHandlerRecordsRequestLog(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: hi\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	fl := &fakeReqLog{}
	h := &Handler{
		auth:              auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:          newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/real"}}}),
		lookup:            &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"real"}, Timeout: 5 * time.Second}}},
		forwarder:         NewStreamingForwarder(),
		log:               logging.NewNopLogger(),
		reqLog:            fl,
		requestLogEnabled: true,
		projectLogLevel:   func(string) config.LogLevel { return config.LogInfo },
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("期望 200，得到 %d", rr.Code)
	}
	entries := fl.snapshot()
	if len(entries) != 1 {
		t.Fatalf("期望 1 条日志，得到 %d", len(entries))
	}
	e := entries[0]
	if e.Project != "p1" || e.Model != "m" || e.Upstream != "cfg1" || e.RealModel != "real" || e.Status != 200 {
		t.Errorf("日志字段不匹配: %+v", e)
	}
	if e.RequestBody != "" || e.ResponseBody != "" {
		t.Errorf("info 级不应记 body: %+v", e)
	}
}

func TestHandlerRecordsRequestLog_DebugBodies(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: hi\n\n"))
	}))
	defer ts.Close()

	fl := &fakeReqLog{}
	h := &Handler{
		auth:              auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:          newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/real"}}}),
		lookup:            &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"real"}, Timeout: 5 * time.Second}}},
		forwarder:         NewStreamingForwarder(),
		log:               logging.NewNopLogger(),
		reqLog:            fl,
		requestLogEnabled: true,
		projectLogLevel:   func(string) config.LogLevel { return config.LogDebug },
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	entries := fl.snapshot()
	if len(entries) != 1 {
		t.Fatalf("期望 1 条日志，得到 %d", len(entries))
	}
	e := entries[0]
	if e.RequestBody == "" || e.ResponseBody == "" {
		t.Errorf("debug 级应记 body: %+v", e)
	}
}

func TestHandlerRecordsRequestLog_OffSkips(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	fl := &fakeReqLog{}
	h := &Handler{
		auth:              auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:          newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/real"}}}),
		lookup:            &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"real"}, Timeout: 5 * time.Second}}},
		forwarder:         NewStreamingForwarder(),
		log:               logging.NewNopLogger(),
		reqLog:            fl,
		requestLogEnabled: true,
		projectLogLevel:   func(string) config.LogLevel { return config.LogOff },
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if len(fl.snapshot()) != 0 {
		t.Errorf("off 级不应记录，得到 %d 条", len(fl.snapshot()))
	}
}

// TestHandlerRecordsRequestLog_ErrorPath 验证 502 失败路径下 reqErrStr 非空，
// 避免失败请求在 request log 中 Error 字段误空，看起来像成功。
// 使用返回普通 error 的 forwarder 触发"所有上游均不可用"路径。
func TestHandlerRecordsRequestLog_ErrorPath(t *testing.T) {
	errFwd := &errorForwarder{err: fmt.Errorf("simulated connection refused")}

	fl := &fakeReqLog{}
	h := &Handler{
		auth:              auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:          newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/real"}}}),
		lookup:            &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: "http://127.0.0.1:1", APIKey: "k", Models: []string{"real"}, Timeout: 5 * time.Second}}},
		forwarder:         errFwd,
		log:               logging.NewNopLogger(),
		reqLog:            fl,
		requestLogEnabled: true,
		projectLogLevel:   func(string) config.LogLevel { return config.LogInfo },
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != 502 {
		t.Fatalf("期望 502，得到 %d", rr.Code)
	}
	entries := fl.snapshot()
	if len(entries) != 1 {
		t.Fatalf("期望 1 条日志，得到 %d", len(entries))
	}
	if entries[0].Error == "" {
		t.Errorf("失败请求应记录非空 Error，得到空（路径: all upstreams failed -> 502）")
	}
}

// TestHandlerRecordsRequestLog_ResponseStartedError 验证流式响应中途失败时
// reqErrStr 非空，对应"upstream failed after response started"路径。
func TestHandlerRecordsRequestLog_ResponseStartedError(t *testing.T) {
	fwd := &startedErrForwarder{written: []byte("data: partial")}

	fl := &fakeReqLog{}
	h := &Handler{
		auth:              auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:          newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/real"}}}),
		lookup:            &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: "http://example.com", APIKey: "k", Models: []string{"real"}, Timeout: 5 * time.Second}}},
		forwarder:         fwd,
		log:               logging.NewNopLogger(),
		reqLog:            fl,
		requestLogEnabled: true,
		projectLogLevel:   func(string) config.LogLevel { return config.LogInfo },
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	entries := fl.snapshot()
	if len(entries) != 1 {
		t.Fatalf("期望 1 条日志，得到 %d", len(entries))
	}
	if entries[0].Error == "" {
		t.Errorf("ResponseStartedError 应记录非空 Error，得到空")
	}
	if entries[0].Error != "" && !strings.Contains(entries[0].Error, "response started") {
		t.Errorf("Error 应包含 'response started'，得到 %q", entries[0].Error)
	}
}

// errorForwarder 始终返回给定 error，不写入响应（非 ResponseStartedError）。
type errorForwarder struct {
	err error
}

func (f *errorForwarder) Forward(cfg config.Upstream, body []byte, headers http.Header, w http.ResponseWriter, c *usage.Collector, log *slog.Logger) error {
	return f.err
}

// TestHandler_5xxFailover_LogsUpstreamErrorDetail 验证上游 5xx 转移时,
// INFO 级补打一条 upstream error response 日志,含 status_code 与 body_head。
func TestHandler_5xxFailover_LogsUpstreamErrorDetail(t *testing.T) {
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok from backup"}`))
	}))
	defer ts2.Close()

	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(503)
		w.Write([]byte(`{"type":"error","error":{"type":"overloaded","message":"service unavailable"}}`))
	}))
	defer ts1.Close()

	var buf bytes.Buffer
	h := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts1.URL, APIKey: "k1", Models: []string{"m1"}, Timeout: 5 * time.Second}, "cfg2": {Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}}},
		forwarder: NewStreamingForwarder(),
		log:       captureLogger(&buf),
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200 after failover, got %d: %s", rec.Code, rec.Body.String())
	}

	logOut := buf.String()
	var detailLine string
	for _, line := range strings.Split(logOut, "\n") {
		if strings.Contains(line, "upstream error response") && !strings.Contains(line, "non-retryable") {
			detailLine = line
			break
		}
	}
	if detailLine == "" {
		t.Fatalf("未找到 upstream error response 详情日志行:\n%s", logOut)
	}
	if !strings.Contains(detailLine, "status_code=503") {
		t.Fatalf("expected status_code=503 in detail line:\n%s", detailLine)
	}
	if !strings.Contains(detailLine, "body_head=") || !strings.Contains(detailLine, "overloaded") {
		t.Fatalf("expected body_head with error content in detail line:\n%s", detailLine)
	}
	if !strings.Contains(detailLine, "upstream=cfg1") {
		t.Fatalf("expected upstream=cfg1 in detail line:\n%s", detailLine)
	}
}
// TestHandler_Forwarded_LogsRealModel 验证成功转发日志包含 real_model 字段，
// 其值为路由解析后的真实上游模型名（与别名 model 不同）。
func TestHandler_Forwarded_LogsRealModel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	var buf bytes.Buffer
	h := &Handler{
		auth:      auth.NewStore(map[string]string{"sk-cp-key1": "p1"}),
		resolver:  newAliasResolver(map[string]map[string][]string{"p1": {"alias-opus": {"cfg1/real-opus"}}}),
		lookup:    &configLookup{upstreams: map[string]config.Upstream{"cfg1": {Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"real-opus"}, Timeout: 5 * time.Second}}},
		forwarder: NewStreamingForwarder(),
		log:       captureLogger(&buf),
		usageEnabled: false,
	}
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"alias-opus"}`))
	req.Header.Set("x-api-key", "sk-cp-key1")
	req.Header.Set("content-type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var forwardedLine string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "request forwarded") {
			forwardedLine = line
			break
		}
	}
	if forwardedLine == "" {
		t.Fatalf("未找到 request forwarded 日志行:\n%s", buf.String())
	}
	if !strings.Contains(forwardedLine, "model=alias-opus") {
		t.Fatalf("日志应含别名 model=alias-opus:\n%s", forwardedLine)
	}
	if !strings.Contains(forwardedLine, "real_model=real-opus") {
		t.Fatalf("日志应含真实模型名 real_model=real-opus:\n%s", forwardedLine)
	}
}
