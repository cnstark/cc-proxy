package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cnstark/cc-proxy/internal/auth"
	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/logging"
	"github.com/cnstark/cc-proxy/internal/project"
)

// modelsTestFixture 组装 /v1/models 测试所需的最小依赖集合。
// 覆盖两个项目：p-direct 开启直连，p-alias 仅别名。
type modelsTestFixture struct {
	handler *ModelsHandler
}

func newModelsFixture(t *testing.T) *modelsTestFixture {
	t.Helper()

	upstreams := []config.Upstream{
		{Name: "up1", URL: "http://up1", APIKey: "k1", Models: []string{"claude-real-a", "claude-real-b"}, Timeout: time.Second},
		{Name: "up2", URL: "http://up2", APIKey: "k2", Models: []string{"claude-real-c"}, Timeout: time.Second},
	}
	projects := []config.Project{
		{
			Name: "p-direct",
			ModelMap: map[string][]string{
				"alias-opus":   {"up1/claude-real-a"},
				"alias-shared": {"up1/claude-real-a", "up2/claude-real-c"},
			},
			AllowDirectAccess: true,
		},
		{
			Name: "p-alias",
			ModelMap: map[string][]string{
				"alias-sonnet": {"up2/claude-real-c"},
			},
			AllowDirectAccess: false,
		},
	}
	cfg := config.Config{
		Server:    config.Server{PrivateKeys: map[string]string{"sk-key-direct": "p-direct", "sk-key-alias": "p-alias"}},
		Upstreams: upstreams,
		Projects:  projects,
	}
	snap := config.NewSnapshot(cfg)

	// 与 hotreload.go 相同的 resolver 构造逻辑
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
		routes[name] = project.ProjectRoute{ModelMap: parsed, AllowDirect: p.AllowDirectAccess}
	}
	resolver := project.NewResolver(routes, snap.ModelUpstreams)

	h := NewModelsHandler(auth.NewStore(snap.Server.PrivateKeys), resolver, &configLookup{upstreams: snap.Upstreams}, &snap, logging.NewNopLogger())
	return &modelsTestFixture{handler: h}
}

// listModels 发起 GET /v1/models 并返回解析后的 data 数组
func listModels(t *testing.T, f *modelsTestFixture, key string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("x-api-key", key)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data    []map[string]any `json:"data"`
		HasMore bool             `json:"has_more"`
		FirstID string           `json:"first_id"`
		LastID  string           `json:"last_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("list: invalid JSON: %v", err)
	}
	if resp.HasMore {
		t.Fatal("list: has_more should be false (no pagination)")
	}
	if len(resp.Data) > 0 {
		if resp.FirstID != resp.Data[0]["id"] || resp.LastID != resp.Data[len(resp.Data)-1]["id"] {
			t.Fatalf("list: first_id/last_id mismatch: %q/%q vs data %q/%q",
				resp.FirstID, resp.LastID, resp.Data[0]["id"], resp.Data[len(resp.Data)-1]["id"])
		}
	}
	return resp.Data
}

func modelIDs(data []map[string]any) map[string]bool {
	ids := make(map[string]bool, len(data))
	for _, m := range data {
		ids[m["id"].(string)] = true
	}
	return ids
}

func TestModelsList_AliasOnly_NoDirectAccess(t *testing.T) {
	f := newModelsFixture(t)
	data := listModels(t, f, "sk-key-alias")
	ids := modelIDs(data)

	// 未开启直连：只返回别名，绝不暴露真实模型名
	if !ids["alias-sonnet"] {
		t.Fatal("alias-sonnet should be listed")
	}
	if len(data) != 1 {
		t.Fatalf("alias-only project should see exactly 1 model, got %v", ids)
	}
	for _, m := range data {
		if m["type"] != "model" || m["id"] == "" || m["created_at"] == "" || m["display_name"] == "" {
			t.Fatalf("model object missing required fields: %v", m)
		}
	}
}

func TestModelsList_DirectAccess_AliasesPlusRealModels(t *testing.T) {
	f := newModelsFixture(t)
	data := listModels(t, f, "sk-key-direct")
	ids := modelIDs(data)

	// 开启直连：别名 + 所有可直接访问的真实模型（含别名映射引用的 claude-real-a/c）
	want := []string{"alias-opus", "alias-shared", "claude-real-a", "claude-real-b", "claude-real-c"}
	for _, id := range want {
		if !ids[id] {
			t.Fatalf("expected model %q in list, got %v", id, ids)
		}
	}
	if len(data) != len(want) {
		t.Fatalf("expected %d models, got %d: %v", len(want), len(data), ids)
	}
	// 稳定排序（按 id）
	for i := 1; i < len(data); i++ {
		if data[i-1]["id"].(string) >= data[i]["id"].(string) {
			t.Fatalf("list not sorted by id: %v", modelIDs(data))
		}
	}
}

func TestModelsGet_Alias_Hit(t *testing.T) {
	f := newModelsFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models/alias-sonnet", nil)
	req.Header.Set("x-api-key", "sk-key-alias")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	if m["id"] != "alias-sonnet" || m["type"] != "model" {
		t.Fatalf("unexpected model object: %v", m)
	}
}

func TestModelsGet_RealModel_DirectAccessProject(t *testing.T) {
	f := newModelsFixture(t)
	// p-direct 开直连：真实模型可查
	req := httptest.NewRequest(http.MethodGet, "/v1/models/claude-real-b", nil)
	req.Header.Set("x-api-key", "sk-key-direct")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestModelsGet_RealModel_AliasOnlyProject_404(t *testing.T) {
	f := newModelsFixture(t)
	// p-alias 未开直连：真实模型名不可见 → 404（不泄露存在性）
	req := httptest.NewRequest(http.MethodGet, "/v1/models/claude-real-c", nil)
	req.Header.Set("x-api-key", "sk-key-alias")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	assertAnthropicError(t, rec, "not_found_error")
}

func TestModelsGet_UnknownModel_404(t *testing.T) {
	f := newModelsFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models/nonexistent", nil)
	req.Header.Set("x-api-key", "sk-key-direct")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestModels_AuthFailure_401(t *testing.T) {
	f := newModelsFixture(t)
	for _, path := range []string{"/v1/models", "/v1/models/alias-opus"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("x-api-key", "bad-key")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d", path, rec.Code)
		}
		assertAnthropicError(t, rec, "authentication_error")
	}
}

func TestModels_BearerTokenAuth(t *testing.T) {
	f := newModelsFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-key-alias")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with Bearer token, got %d", rec.Code)
	}
}

func TestModels_PostMethod_405(t *testing.T) {
	f := newModelsFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	req.Header.Set("x-api-key", "sk-key-direct")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestModelsList_IgnoresPaginationParams(t *testing.T) {
	f := newModelsFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?limit=1&after_id=alias-opus", nil)
	req.Header.Set("x-api-key", "sk-key-direct")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	// 忽略分页参数，返回全量
	if len(resp.Data) != 5 {
		t.Fatalf("expected 5 models ignoring pagination, got %d", len(resp.Data))
	}
}

// assertAnthropicError 断言响应为 Anthropic 统一错误格式且 type 匹配
func assertAnthropicError(t *testing.T, rec *httptest.ResponseRecorder, errType string) {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("error response not JSON: %v", err)
	}
	if resp["type"] != "error" {
		t.Fatalf("expected type=error, got %v", resp)
	}
	e, ok := resp["error"].(map[string]any)
	if !ok || e["type"] != errType {
		t.Fatalf("expected error.type=%q, got %v", errType, resp["error"])
	}
}
