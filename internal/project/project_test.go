package project

import (
	"reflect"
	"testing"
)

// newResolver 构造测试用 resolver：给定预解析路由 + modelUpstreams（真实模型名 → 有序 upstream 名）。
func newResolver(projects map[string]ProjectRoute, modelUpstreams map[string][]string) *ModelResolver {
	return NewResolver(projects, modelUpstreams)
}

func TestResolve_AliasSuccess(t *testing.T) {
	r := newResolver(
		map[string]ProjectRoute{
			"project1": {ModelMap: map[string][]ResolvedTarget{
				"modelA": {{Upstream: "cfg1", Model: "m1"}, {Upstream: "cfg2", Model: "m2"}},
				"modelB": {{Upstream: "cfg3", Model: "m3"}},
			}},
		},
		nil,
	)
	got, ok := r.Resolve("project1", "modelA")
	if !ok {
		t.Fatal("expected resolve success")
	}
	want := []ResolvedTarget{{Upstream: "cfg1", Model: "m1"}, {Upstream: "cfg2", Model: "m2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestResolve_UnknownProject(t *testing.T) {
	r := newResolver(map[string]ProjectRoute{}, nil)
	_, ok := r.Resolve("nonexistent", "modelA")
	if ok {
		t.Fatal("expected resolve failure for unknown project")
	}
}

func TestResolve_UnknownModel_NoDirect(t *testing.T) {
	r := newResolver(
		map[string]ProjectRoute{
			"project1": {ModelMap: map[string][]ResolvedTarget{"modelA": {{Upstream: "cfg1", Model: "m1"}}}},
		},
		nil,
	)
	_, ok := r.Resolve("project1", "modelB")
	if ok {
		t.Fatal("expected resolve failure for unknown model without direct access")
	}
}

func TestResolve_DirectAccess_Hit(t *testing.T) {
	// 直接访问：请求 model = 真实模型名 "m2"，应返回提供 m2 的 upstream（cfg2），model=请求名
	r := newResolver(
		map[string]ProjectRoute{
			"p1": {AllowDirect: true, ModelMap: map[string][]ResolvedTarget{"aliasA": {{Upstream: "cfg1", Model: "m1"}}}},
		},
		map[string][]string{"m1": {"cfg1"}, "m2": {"cfg2"}},
	)
	got, ok := r.Resolve("p1", "m2")
	if !ok {
		t.Fatal("expected direct access resolve success")
	}
	want := []ResolvedTarget{{Upstream: "cfg2", Model: "m2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestResolve_DirectAccess_Disabled(t *testing.T) {
	r := newResolver(
		map[string]ProjectRoute{
			"p1": {AllowDirect: false, ModelMap: map[string][]ResolvedTarget{"aliasA": {{Upstream: "cfg1", Model: "m1"}}}},
		},
		map[string][]string{"m2": {"cfg2"}},
	)
	_, ok := r.Resolve("p1", "m2")
	if ok {
		t.Fatal("expected resolve failure when direct access disabled and model is not an alias")
	}
}

func TestResolve_DirectAccess_NonUpstreamName(t *testing.T) {
	// 请求一个不在 modelUpstreams 里的模型名 → miss
	r := newResolver(
		map[string]ProjectRoute{
			"p1": {AllowDirect: true, ModelMap: map[string][]ResolvedTarget{"aliasA": {{Upstream: "cfg1", Model: "m1"}}}},
		},
		map[string][]string{"m1": {"cfg1"}},
	)
	_, ok := r.Resolve("p1", "not-a-real-model")
	if ok {
		t.Fatal("expected resolve failure for non-alias non-real-model name even with direct access")
	}
}

func TestResolve_AliasPreferredOverDirect(t *testing.T) {
	// 防御性：别名 "m1" 与真实模型名 "m1" 同名，验证 alias 优先（modelUpstreams 含 m1，但 alias 命中先返回）。
	r := newResolver(
		map[string]ProjectRoute{
			"p1": {AllowDirect: true, ModelMap: map[string][]ResolvedTarget{"m1": {{Upstream: "cfg1", Model: "m1"}, {Upstream: "cfg2", Model: "m2"}}}},
		},
		map[string][]string{"m1": {"cfg1", "cfg2"}, "m2": {"cfg2"}},
	)
	got, ok := r.Resolve("p1", "m1")
	if !ok {
		t.Fatal("expected alias resolve success")
	}
	want := []ResolvedTarget{{Upstream: "cfg1", Model: "m1"}, {Upstream: "cfg2", Model: "m2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v from alias, got %v", want, got)
	}
}
