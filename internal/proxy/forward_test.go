package proxy

import (
	"bytes"
	"encoding/json"
	"github.com/cnstark/claude-switch/internal/auth"
	"github.com/cnstark/claude-switch/internal/config"
	"github.com/cnstark/claude-switch/internal/logging"
	"github.com/cnstark/claude-switch/internal/usage"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRewriteRequest_ModelReplaced(t *testing.T) {
	body := []byte(`{"model":"aliasModel","max_tokens":100}`)
	newBody, err := rewriteRequestBody(body, "real-model-name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	json.Unmarshal(newBody, &parsed)
	if parsed["model"] != "real-model-name" {
		t.Fatalf("expected model 'real-model-name', got %v", parsed["model"])
	}
	if parsed["max_tokens"] != float64(100) {
		t.Fatal("other fields should be preserved")
	}
}

// TestRewriteRequest_NoHTMLEscape 验证 rewriteRequestBody 不应把请求体里的
// HTML 特殊字符（<、>、&）转义成 < 等。这些字符在 Claude Code 的
// system-reminder / 工具描述里很常见，转义虽 JSON 语义等价，但会改变字节
// 内容并可能触发某些上游解析器的边界问题。原样保留更安全。
func TestRewriteRequest_NoHTMLEscape(t *testing.T) {
	body := []byte(`{"model":"alias","messages":[{"content":"<system-reminder>x<y&z</system-reminder>"}]}`)
	newBody, err := rewriteRequestBody(body, "real-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := string(newBody)
	// 不应出现 HTML 转义形式 < / > / &
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, esc) {
			t.Fatalf("rewrite must not HTML-escape: found %s in output:\n%s", esc, got)
		}
	}
	// 原始的 <、>、& 应原样保留
	if !strings.Contains(got, "<system-reminder>") {
		t.Fatalf("expected raw <system-reminder> preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "x<y&z") {
		t.Fatalf("expected raw x<y&z preserved, got:\n%s", got)
	}
}

func TestRewriteHeaders_KeyReplaced(t *testing.T) {
	h := http.Header{
		"X-Api-Key":         []string{"old-key"},
		"Content-Type":      []string{"application/json"},
		"Anthropic-Version": []string{"2023-06-01"},
	}
	rewriteHeaders(h, "new-upstream-key")
	if h.Get("X-Api-Key") != "new-upstream-key" {
		t.Fatalf("expected X-Api-Key to be replaced, got %s", h.Get("X-Api-Key"))
	}
	if h.Get("Content-Type") != "application/json" {
		t.Fatal("Content-Type should be preserved")
	}
}

func TestForward_BasicPassthrough(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-custom", "echo")
		w.WriteHeader(200)
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		m["upstream_received"] = true
		json.NewEncoder(w).Encode(m)
	}))
	defer ts.Close()

	cfg := config.Upstream{
		Name: "cfg1", URL: ts.URL, APIKey: "sk-upstream",
		Models: []string{"real-model"}, Timeout: 5 * time.Second,
	}

	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"aliasModel": {"cfg1/real-model"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()

	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"aliasModel","max_tokens":100}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("x-custom") != "echo" {
		t.Fatal("expected custom header to be passed through")
	}
}

func TestForward_StreamingSSE(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		for i := 0; i < 3; i++ {
			w.Write([]byte("data: chunk" + string(rune('0'+i)) + "\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	cfg := config.Upstream{Name: "cfg1", URL: ts.URL, APIKey: "sk-upstream", Models: []string{"real-model"}, Timeout: 5 * time.Second}
	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/real-model"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","stream":true}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: chunk0") {
		t.Fatal("expected streaming chunks in response")
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatal("expected [DONE] marker")
	}
}

// TestFailover_ResponseBodyStarted_NoFailover 验证故障转移不变量：
// 第一个上游在 WriteHeader 之后、读 body 阶段失败（模拟响应已开始后超时/读错），
// proxy 绝不能转移到第二个上游——否则会把两段响应拼给客户端，导致
// 客户端解析到截断的 JSON（unexpected end of JSON input）。
func TestFailover_ResponseBodyStarted_NoFailover(t *testing.T) {
	// 第二个上游：仅当被错误地调用时才会收到请求
	secondCalled := false
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"second":"should-not-be-merged"}`))
	}))
	defer ts2.Close()

	// 第一个上游：先 WriteHeader(200) 并写一段响应体，然后模拟读 body 阶段失败
	// 用一个会在写首字节后 hang 住直到客户端超时的 server
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		// 已提交响应头 + 写了首字节 → 响应已开始
		w.Write([]byte("data: partial-from-first\n\n"))
		flusher.Flush()
		// 然后挂起，让 client 的 Timeout 在读 body 阶段触发
		<-r.Context().Done()
	}))
	defer ts1.Close()

	cfg1 := config.Upstream{Name: "cfg1", URL: ts1.URL, APIKey: "k1", Models: []string{"m1"}, Timeout: 200 * time.Millisecond}
	cfg2 := config.Upstream{Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}

	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	// 不变量：响应已开始后不得转移到第二个上游
	if secondCalled {
		t.Fatal("FAIL: proxy transferred to second upstream AFTER response body had started — " +
			"this merges two responses and corrupts the client stream (root cause of 'unexpected end of JSON input')")
	}
	// 客户端应只看到第一个上游的部分响应，不被第二段污染
	if strings.Contains(body, "should-not-be-merged") {
		t.Fatalf("FAIL: client received merged response from two upstreams:\n%s", body)
	}
}

func TestFailover_FirstFails_FallbackSucceeds(t *testing.T) {
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok from backup"}`))
	}))
	defer ts2.Close()

	cfg1 := config.Upstream{Name: "cfg1", URL: "http://127.0.0.1:19999", APIKey: "k1", Models: []string{"m1"}, Timeout: 100 * time.Millisecond}
	cfg2 := config.Upstream{Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}

	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected fallback to cfg2 succeed with 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok from backup") {
		t.Fatal("expected response from backup upstream")
	}
}

func TestFailover_AllFail_502(t *testing.T) {
	cfg1 := config.Upstream{Name: "cfg1", URL: "http://127.0.0.1:19998", APIKey: "k1", Models: []string{"m1"}, Timeout: 50 * time.Millisecond}
	cfg2 := config.Upstream{Name: "cfg2", URL: "http://127.0.0.1:19999", APIKey: "k2", Models: []string{"m2"}, Timeout: 50 * time.Millisecond}

	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 502 {
		t.Fatalf("expected 502 when all upstreams fail, got %d", rec.Code)
	}
	var errResp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp["type"] != "error" {
		t.Fatal("expected Anthropic error format for 502")
	}
}

// usageFakeRecorder 供 proxy 包测试用：捕获 Record 调用。
type usageFakeRecorder struct {
	project string
	model   string
	date    string
	u       usage.TokenUsage
	calls   int
}

func (f *usageFakeRecorder) Record(project, model, date string, u usage.TokenUsage) {
	f.project, f.model, f.date, f.u = project, model, date, u
	f.calls++
}

func TestForward_UsageRecordedFromSSE(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12500,\"cache_creation_input_tokens\":800,\"cache_read_input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		flusher.Flush()
		w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":3200}}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	cfg := config.Upstream{Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"real"}, Timeout: 5 * time.Second}
	rec := &usageFakeRecorder{}
	c := usage.NewCollector(rec, "p1", "aliasModel")

	fwd := NewStreamingForwarder()
	w := httptest.NewRecorder()
	err := fwd.Forward(cfg, []byte(`{}`), http.Header{"content-type": []string{"application/json"}}, w, c, nil)
	if err != nil {
		t.Fatalf("forward error: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected 1 Record call, got %d", rec.calls)
	}
	if rec.u.Input != 12500 || rec.u.Output != 3200 || rec.u.CacheCreation != 800 || rec.u.CacheRead != 0 {
		t.Fatalf("unexpected recorded usage: %+v", rec.u)
	}
	if rec.project != "p1" || rec.model != "aliasModel" {
		t.Fatalf("unexpected project/model: %s/%s", rec.project, rec.model)
	}
}

func TestForward_UsageStreamByteIdenticalToNoCollector(t *testing.T) {
	// 关键不变量：旁路 scanner 不改变客户端收到的字节
	sse := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")

	runOnce := func(c *usage.Collector) []byte {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher := w.(http.Flusher)
			w.Header().Set("content-type", "text/event-stream")
			w.WriteHeader(200)
			w.Write(sse)
			flusher.Flush()
		}))
		defer ts.Close()
		cfg := config.Upstream{Name: "cfg1", URL: ts.URL, APIKey: "k", Models: []string{"m"}, Timeout: 5 * time.Second}
		fwd := NewStreamingForwarder()
		rec := httptest.NewRecorder()
		_ = fwd.Forward(cfg, []byte(`{}`), http.Header{}, rec, c, nil)
		return rec.Body.Bytes()
	}

	without := runOnce(nil)
	rec := &usageFakeRecorder{}
	with := runOnce(usage.NewCollector(rec, "p1", "m"))
	if !bytes.Equal(without, with) {
		t.Fatalf("collector changed client stream:\nwithout=%s\nwith=%s", without, with)
	}
	if rec.calls != 1 {
		t.Fatalf("expected usage recorded with collector, got %d calls", rec.calls)
	}
}

func TestForward_ConnectionFailure_NoCommit(t *testing.T) {
	cfg := config.Upstream{Name: "cfg1", URL: "http://127.0.0.1:19997", APIKey: "k", Models: []string{"m"}, Timeout: 50 * time.Millisecond}
	rec := &usageFakeRecorder{}
	c := usage.NewCollector(rec, "p1", "m")
	fwd := NewStreamingForwarder()
	w := httptest.NewRecorder()
	_ = fwd.Forward(cfg, []byte(`{}`), http.Header{}, w, c, nil)
	if rec.calls != 0 {
		t.Fatalf("expected no commit on connection failure, got %d", rec.calls)
	}
}

// TestFailover_FirstReturns5xx_FallbackSucceeds 验证第一个上游返回 5xx 时故障转移到第二个
func TestFailover_FirstReturns5xx_FallbackSucceeds(t *testing.T) {
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

	cfg1 := config.Upstream{Name: "cfg1", URL: ts1.URL, APIKey: "k1", Models: []string{"m1"}, Timeout: 5 * time.Second}
	cfg2 := config.Upstream{Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}

	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected fallback to cfg2 succeed with 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok from backup") {
		t.Fatal("expected response from backup upstream after 5xx failover")
	}
}

// TestFailover_FirstReturns429_FallbackSucceeds 验证第一个上游返回 429 时故障转移到第二个
func TestFailover_FirstReturns429_FallbackSucceeds(t *testing.T) {
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok from backup"}`))
	}))
	defer ts2.Close()

	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(429)
		w.Write([]byte(`{"type":"error","error":{"type":"rate_limit","message":"too many requests"}}`))
	}))
	defer ts1.Close()

	cfg1 := config.Upstream{Name: "cfg1", URL: ts1.URL, APIKey: "k1", Models: []string{"m1"}, Timeout: 5 * time.Second}
	cfg2 := config.Upstream{Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}

	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected fallback to cfg2 succeed with 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok from backup") {
		t.Fatal("expected response from backup upstream after 429 failover")
	}
}

// TestFailover_FirstReturns401_NoFailover 验证第一个上游返回 401（不可重试）时不进行故障转移
func TestFailover_FirstReturns401_NoFailover(t *testing.T) {
	secondCalled := false
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"should not reach here"}`))
	}))
	defer ts2.Close()

	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(401)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`))
	}))
	defer ts1.Close()

	cfg1 := config.Upstream{Name: "cfg1", URL: ts1.URL, APIKey: "k1", Models: []string{"m1"}, Timeout: 5 * time.Second}
	cfg2 := config.Upstream{Name: "cfg2", URL: ts2.URL, APIKey: "k2", Models: []string{"m2"}, Timeout: 5 * time.Second}

	authStore := auth.NewStore(map[string]string{"sk-cs-key1": "p1"})
	resolver := newAliasResolver(map[string]map[string][]string{"p1": {"m": {"cfg1/m1", "cfg2/m2"}}})
	lookup := &configLookup{upstreams: map[string]config.Upstream{"cfg1": cfg1, "cfg2": cfg2}}
	fwd := NewStreamingForwarder()
	log := logging.NewNopLogger()
	h := NewHandler(authStore, resolver, lookup, fwd, log)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", "sk-cs-key1")
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Fatalf("expected 401 to be passed through, got %d", rec.Code)
	}
	if secondCalled {
		t.Fatal("failover should not happen for 401 error")
	}
	if !strings.Contains(rec.Body.String(), "authentication_error") {
		t.Fatal("expected 401 error body to be passed through")
	}
}
