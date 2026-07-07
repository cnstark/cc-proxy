package config

import (
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/cnstark/claude-switch/internal/logging"
)

func intPtr(v int) *int { return &v }

func TestValidate_Success(t *testing.T) {
	cfg := Config{
		Server: Server{
			Listen:     "127.0.0.1:8787",
			LogLevel:   LogInfo,
			LogMaxDays: intPtr(7),
			PrivateKeys: map[string]string{
				"sk-cs-key1": "project1",
			},
		},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://api.anthropic.com", APIKey: "sk-ant-xxx", Models: []string{"claude-opus-4-8"}, Timeout: 60 * time.Second},
			{Name: "cfg2", URL: "https://other.com", APIKey: "sk-xxx", Models: []string{"claude-sonnet-4-6"}, Timeout: 30 * time.Second},
		},
		Projects: []Project{
			{
				Name: "project1", LogLevel: LogMeta,
				ModelMap: map[string][]string{
					"modelA": {"cfg1/claude-opus-4-8", "cfg2/claude-sonnet-4-6"},
				},
			},
		},
	}
	err := Validate(cfg)
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidate_DuplicateUpstreamName(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
			{Name: "cfg1", URL: "https://b.com", APIKey: "k2", Models: []string{"m2"}, Timeout: 30 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate upstream name")
	}
}

func TestValidate_DanglingUpstreamRef(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1", "cfg_nonexistent/m1"}}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for dangling upstream reference")
	}
}

func TestValidate_NoPrivateKeys(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787"},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for no private keys configured")
	}
}

func TestValidate_PrivateKeyPointsToMissingProject(t *testing.T) {
	cfg := Config{
		Server: Server{
			Listen:      "127.0.0.1:8787",
			PrivateKeys: map[string]string{"sk-cs-key1": "nonexistent_project"},
		},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for private key pointing to missing project")
	}
}

func TestValidate_DuplicateProjectName(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
			{Name: "p1", ModelMap: map[string][]string{"m2": {"cfg1/m1"}}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate project name")
	}
}

func TestValidate_DuplicateModelMapKey(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"modelA": {"cfg1/m1"}}},
		},
	}
	err := Validate(cfg)
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestValidate_EmptyUpstreamName(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for empty upstream name")
	}
}

func TestValidate_DuplicatePrivateKey(t *testing.T) {
	cfg := Config{
		Server: Server{
			Listen: "127.0.0.1:8787",
			// 构造一个实际只含一个 key 的 map（Go 语法不允许 literal 重复 key）
			PrivateKeys: map[string]string{"sk-cs-key1": "p2"},
		},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
			{Name: "p2", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	// Go 语法不允许 literal 重复 key，因此无法在编译期构造出重复 private key 的情形。
	// 此测试仅验证单 key 指向存在的项目时通过校验。
	err := Validate(cfg)
	if err != nil {
		t.Fatalf("expected valid with one key, got: %v", err)
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	yamlData := `
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://api.anthropic.com
    apikey: sk-ant-xxx
    models: [claude-opus-4-8]
    timeout: 60s
projects:
  - name: project1
    log_level: meta
    model_map:
      modelA: [cfg1/claude-opus-4-8]
`
	path := t.TempDir() + "/config.yaml"

	snap, err := Load([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if snap.Server.Listen != "127.0.0.1:8787" {
		t.Fatalf("expected listen 127.0.0.1:8787, got %s", snap.Server.Listen)
	}
	if len(snap.Upstreams) != 1 {
		t.Fatalf("expected 1 upstream, got %d", len(snap.Upstreams))
	}
	if snap.Upstreams["cfg1"].Models[0] != "claude-opus-4-8" {
		t.Fatalf("expected model claude-opus-4-8, got %s", snap.Upstreams["cfg1"].Models[0])
	}

	// Test Save (atomic write)
	err = Save(snap.Raw, path)
	if err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}

	// Reload saved file to verify round-trip
	snap2, err := LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	if snap2.Server.Listen != snap.Server.Listen {
		t.Fatal("round-trip mismatch")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	_, err := Load([]byte(`not: [valid: yaml`))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_ValidationError(t *testing.T) {
	yamlData := `
server:
  listen: 127.0.0.1:8787
  private_keys: {}
upstreams: []
projects: []
`
	_, err := Load([]byte(yamlData))
	if err == nil {
		t.Fatal("expected validation error for missing private keys")
	}
}

func TestWatcher_DetectChange(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/config.yaml"

	initialYAML := `
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://a.com
    apikey: k1
    models: [m1]
    timeout: 60s
projects:
  - name: project1
    log_level: off
    model_map:
      modelA: [cfg1/m1]
`
	if err := os.WriteFile(path, []byte(initialYAML), 0600); err != nil {
		t.Fatal(err)
	}

	w := NewWatcher(path, 50*time.Millisecond, logging.NewStdErrLogger(slog.LevelWarn))
	snap, err := w.Current()
	if err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	if snap.Upstreams["cfg1"].Models[0] != "m1" {
		t.Fatalf("expected model m1, got %s", snap.Upstreams["cfg1"].Models[0])
	}

	// Modify file
	updatedYAML := `
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://a.com
    apikey: k1
    models: [m2-updated]
    timeout: 60s
projects:
  - name: project1
    log_level: off
    model_map:
      modelA: [cfg1/m2-updated]
`
	time.Sleep(100 * time.Millisecond) // ensure mtime differs
	if err := os.WriteFile(path, []byte(updatedYAML), 0600); err != nil {
		t.Fatal(err)
	}

	// Wait for watcher to detect change
	time.Sleep(300 * time.Millisecond)

	snap2, err := w.Current()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if snap2.Upstreams["cfg1"].Models[0] != "m2-updated" {
		t.Fatalf("expected model m2-updated after reload, got %s", snap2.Upstreams["cfg1"].Models[0])
	}
	w.Stop()
}

func TestWatcher_InvalidConfigKeepsOld(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/config.yaml"

	validYAML := `
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://a.com
    apikey: k1
    models: [m1]
    timeout: 60s
projects:
  - name: project1
    log_level: off
    model_map:
      modelA: [cfg1/m1]
`
	os.WriteFile(path, []byte(validYAML), 0600)

	w := NewWatcher(path, 50*time.Millisecond, logging.NewStdErrLogger(slog.LevelWarn))
	defer w.Stop()
	_, err := w.Current()
	if err != nil {
		t.Fatal(err)
	}

	// Write invalid config
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(path, []byte(`invalid: [yaml`), 0600)
	time.Sleep(300 * time.Millisecond)

	snap, err := w.Current()
	if err != nil {
		t.Fatal("expected old config to be retained")
	}
	if snap.Upstreams["cfg1"].Models[0] != "m1" {
		t.Fatal("expected old config to be preserved after invalid reload")
	}
}

func TestWatcher_GetSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/config.yaml"
	validYAML := `
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://a.com
    apikey: k1
    models: [m1]
    timeout: 60s
projects:
  - name: project1
    log_level: off
    model_map:
      modelA: [cfg1/m1]
`
	os.WriteFile(path, []byte(validYAML), 0600)

	w := NewWatcher(path, 50*time.Millisecond, logging.NewStdErrLogger(slog.LevelWarn))
	defer w.Stop()
	snap := w.GetSnapshot()
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
}

func TestLoad_UsageStats(t *testing.T) {
	// 显式 true
	snap, err := Load([]byte(`
server:
  listen: 127.0.0.1:8787
  usage_stats: true
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://a.com
    apikey: k1
    models: [m1]
    timeout: 60s
projects:
  - name: project1
    model_map:
      modelA: [cfg1/m1]
`))
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if !snap.Server.UsageStats {
		t.Fatal("expected usage_stats=true after load")
	}

	// 默认 false（缺省字段）
	snap2, err := Load([]byte(`
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - {name: cfg1, url: https://a.com, apikey: k1, models: [m1], timeout: 60s}
projects:
  - {name: project1, model_map: {modelA: [cfg1/m1]}}
`))
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if snap2.Server.UsageStats {
		t.Fatal("expected usage_stats default false")
	}
}

func TestValidate_RetryBackoff_Valid(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second,
				RetryBackoff: []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid retry_backoff, got: %v", err)
	}
}

func TestValidate_RetryBackoff_TooManyTiers(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second,
				RetryBackoff: []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for >4 retry_backoff tiers")
	}
}

func TestValidate_RetryBackoff_NegativeDuration(t *testing.T) {
	cfg := Config{
		Server: Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second,
				RetryBackoff: []time.Duration{30 * time.Second, -1 * time.Second}},
		},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for negative retry_backoff duration")
	}
}

func TestLoad_RetryBackoff_RoundTrip(t *testing.T) {
	yamlData := `
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://api.anthropic.com
    apikey: sk-ant-xxx
    models: [claude-opus-4-8]
    timeout: 60s
    retry_backoff: [30s, 2m, 5m, 15m]
projects:
  - name: project1
    log_level: off
    model_map:
      modelA: [cfg1/claude-opus-4-8]
`
	snap, err := Load([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	cfg1 := snap.Upstreams["cfg1"]
	if len(cfg1.RetryBackoff) != 4 {
		t.Fatalf("expected 4 backoff tiers, got %d", len(cfg1.RetryBackoff))
	}
	if cfg1.RetryBackoff[0] != 30*time.Second {
		t.Fatalf("expected T1=30s, got %s", cfg1.RetryBackoff[0])
	}
	if cfg1.RetryBackoff[1] != 2*time.Minute {
		t.Fatalf("expected T2=2m, got %s", cfg1.RetryBackoff[1])
	}
	if cfg1.RetryBackoff[2] != 5*time.Minute {
		t.Fatalf("expected T3=5m, got %s", cfg1.RetryBackoff[2])
	}
	if cfg1.RetryBackoff[3] != 15*time.Minute {
		t.Fatalf("expected T4=15m, got %s", cfg1.RetryBackoff[3])
	}
}

func TestLoad_RetryBackoff_DefaultEmpty(t *testing.T) {
	yamlData := `
server:
  listen: 127.0.0.1:8787
  private_keys:
    sk-cs-key1: project1
upstreams:
  - name: cfg1
    url: https://api.anthropic.com
    apikey: sk-ant-xxx
    models: [claude-opus-4-8]
    timeout: 60s
projects:
  - name: project1
    log_level: off
    model_map:
      modelA: [cfg1/claude-opus-4-8]
`
	snap, err := Load([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	cfg1 := snap.Upstreams["cfg1"]
	if len(cfg1.RetryBackoff) != 0 {
		t.Fatalf("expected empty retry_backoff by default, got %d", len(cfg1.RetryBackoff))
	}
}

func TestNewSnapshot_ProjectLogLevelDefaults(t *testing.T) {
	// 回归测试：确保 NewSnapshot 的 ps map 构建在 project 默认值应用之后，
	// 使得 snapshot.Projects[name].LogLevel 与 snapshot.Raw.Projects[i].LogLevel 一致。
	cfg := Config{
		Server: Server{
			Listen: "127.0.0.1:8787",
			PrivateKeys: map[string]string{
				"sk-cs-key1": "p1",
				"sk-cs-key2": "p2",
			},
		},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second},
		},
		Projects: []Project{
			{Name: "p1", LogLevel: LogMeta, ModelMap: map[string][]string{"modelA": {"cfg1/m1"}}},
			{Name: "p2", LogLevel: "", ModelMap: map[string][]string{"modelA": {"cfg1/m1"}}},
		},
	}

	snap := NewSnapshot(cfg)

	// meta 应被映射为 info
	if snap.Projects["p1"].LogLevel != LogInfo {
		t.Errorf("expected p1.LogLevel=%q (meta→info), got %q", LogInfo, snap.Projects["p1"].LogLevel)
	}
	// 空值应被填充为 off
	if snap.Projects["p2"].LogLevel != LogOff {
		t.Errorf("expected p2.LogLevel=%q (default off), got %q", LogOff, snap.Projects["p2"].LogLevel)
	}

	// 验证一致性：snapshot.Projects 与 snapshot.Raw.Projects 的值一致
	for i, p := range snap.Raw.Projects {
		if snap.Projects[p.Name].LogLevel != p.LogLevel {
			t.Errorf("inconsistency: snapshot.Projects[%q].LogLevel=%q != snapshot.Raw.Projects[%d].LogLevel=%q",
				p.Name, snap.Projects[p.Name].LogLevel, i, p.LogLevel)
		}
	}
}

func TestValidate_ServerLogLevel_Invalid(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", LogLevel: "invalid", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second}},
		Projects:  []Project{{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for invalid server log_level")
	}
}

func TestValidate_LogMaxDays_Negative(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", LogLevel: LogInfo, LogMaxDays: intPtr(-1), PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second}},
		Projects:  []Project{{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for negative log_max_days")
	}
}

func TestValidate_ModelMapAliasCanEqualUpstreamName(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second}},
		Projects: []Project{
			{Name: "p1", ModelMap: map[string][]string{"cfg1": {"cfg1/m1"}}},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid (alias may equal upstream name), got: %v", err)
	}
}

func TestValidate_AllowDirectAccess_TrueIsValid(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second}},
		Projects: []Project{
			{Name: "p1", AllowDirectAccess: true, ModelMap: map[string][]string{"aliasA": {"cfg1/m1"}}},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid config with allow_direct_access=true, got: %v", err)
	}
}

func TestValidate_UpstreamNameContainsSlash(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg/1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second}},
		Projects:  []Project{{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for upstream name containing /")
	}
}

func TestValidate_ModelsEmpty(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: nil, Timeout: 60 * time.Second}},
		Projects:  []Project{{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for empty models")
	}
}

func TestValidate_ModelsDuplicate(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1", "m1"}, Timeout: 60 * time.Second}},
		Projects:  []Project{{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for duplicate models")
	}
}

func TestValidate_ModelMapBadFormat(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second}},
		Projects:  []Project{{Name: "p1", ModelMap: map[string][]string{"alias": {"cfg1"}}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for model_map bad format (missing /)")
	}
}

func TestValidate_ModelMapModelNotServed(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1"}, Timeout: 60 * time.Second}},
		Projects:  []Project{{Name: "p1", ModelMap: map[string][]string{"alias": {"cfg1/nonexistent"}}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for model_map referencing model not served by upstream")
	}
}

func TestNewSnapshot_ModelUpstreamsIndex(t *testing.T) {
	cfg := Config{
		Server:    Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cs-key1": "p1"}},
		Upstreams: []Upstream{
			{Name: "cfg1", URL: "https://a.com", APIKey: "k1", Models: []string{"m1", "m2"}, Timeout: 60 * time.Second},
			{Name: "cfg2", URL: "https://b.com", APIKey: "k2", Models: []string{"m2"}, Timeout: 30 * time.Second},
		},
		Projects: []Project{{Name: "p1", ModelMap: map[string][]string{"m": {"cfg1/m1"}}}},
	}
	snap := NewSnapshot(cfg)
	expected := map[string][]string{
		"m1": {"cfg1"},
		"m2": {"cfg1", "cfg2"},
	}
	if !reflect.DeepEqual(snap.ModelUpstreams, expected) {
		t.Fatalf("ModelUpstreams mismatch.\n  got:  %v\n  want: %v", snap.ModelUpstreams, expected)
	}
}
