package admin

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cnstark/cc-proxy/internal/config"
)

func writeConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Config{
		Server:    config.Server{Listen: "127.0.0.1:8787", PrivateKeys: map[string]string{"sk-cp-secret-key": "default"}},
		Upstreams: []config.Upstream{{Name: "u1", URL: "https://api.example.com", APIKey: "sk-ant-real-key", Models: []string{"real"}, Timeout: 60 * time.Second}},
		Projects:  []config.Project{{Name: "default", LogLevel: config.LogInfo, ModelMap: map[string][]string{"alias": {"u1/real"}}}},
	}
	if err := config.Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return path
}

func TestConfigGetMasksSecrets(t *testing.T) {
	s := &Server{configPath: writeConfigFile(t), enabled: true}
	req := httptest.NewRequest("GET", "/api/config", nil)
	rr := httptest.NewRecorder()
	s.handleConfigGet(rr, req)
	if rr.Code != 200 {
		t.Fatalf("期望 200，得到 %d", rr.Code)
	}
	body := rr.Body.String()
	if bytes.Contains([]byte(body), []byte("sk-ant-real-key")) {
		t.Errorf("apikey 未脱敏")
	}
	if bytes.Contains([]byte(body), []byte("sk-cp-secret-key")) {
		t.Errorf("private key 未脱敏")
	}
}

func TestConfigPutPreservesMaskedApikey(t *testing.T) {
	path := writeConfigFile(t)
	s := &Server{configPath: path, enabled: true}
	// GET 拿到脱敏配置
	rr := httptest.NewRecorder()
	s.handleConfigGet(rr, httptest.NewRequest("GET", "/api/config", nil))
	var masked config.Config
	json.Unmarshal(rr.Body.Bytes(), &masked)
	// 直接回传脱敏值（模拟前端原样回传）
	out, _ := json.Marshal(masked)
	putRR := httptest.NewRecorder()
	s.handleConfigPut(putRR, httptest.NewRequest("PUT", "/api/config", bytes.NewReader(out)))
	if putRR.Code != 200 {
		t.Fatalf("期望 200，得到 %d body=%s", putRR.Code, putRR.Body.String())
	}
	snap, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if snap.Upstreams["u1"].APIKey != "sk-ant-real-key" {
		t.Errorf("脱敏占位应保留原 apikey，得到 %q", snap.Upstreams["u1"].APIKey)
	}
	if snap.Server.PrivateKeys["sk-cp-secret-key"] != "default" {
		t.Errorf("private key 未还原，得到 %v", snap.Server.PrivateKeys)
	}
}

func TestConfigPutRejectsInvalid(t *testing.T) {
	s := &Server{configPath: writeConfigFile(t), enabled: true}
	bad := map[string]any{"server": map[string]any{}, "upstreams": []any{}, "projects": []any{}}
	out, _ := json.Marshal(bad)
	rr := httptest.NewRecorder()
	s.handleConfigPut(rr, httptest.NewRequest("PUT", "/api/config", bytes.NewReader(out)))
	if rr.Code != 422 {
		t.Errorf("期望 422，得到 %d", rr.Code)
	}
	// 确认原文件未被覆盖
	snap2, err := config.LoadFile(s.configPath)
	if err != nil {
		t.Fatalf("LoadFile after rejected PUT: %v", err)
	}
	if snap2.Upstreams["u1"].APIKey != "sk-ant-real-key" {
		t.Errorf("拒绝的 PUT 不应写盘，原 apikey 应保留，得到 %q", snap2.Upstreams["u1"].APIKey)
	}
}

func TestProjectUpdateReturns404ForMissing(t *testing.T) {
	s := &Server{configPath: writeConfigFile(t), enabled: true}
	body := strings.NewReader(`{"log_level":"debug"}`)
	req := httptest.NewRequest("PUT", "/api/projects/nonexistent", body)
	req.SetPathValue("name", "nonexistent")
	rr := httptest.NewRecorder()
	s.handleProjectUpdate(rr, req)
	if rr.Code != 404 {
		t.Errorf("期望 404，得到 %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMappingDeleteReturns404ForMissingProject(t *testing.T) {
	s := &Server{configPath: writeConfigFile(t), enabled: true}
	req := httptest.NewRequest("DELETE", "/api/projects/nonexistent/mappings/foo", nil)
	req.SetPathValue("name", "nonexistent")
	req.SetPathValue("model", "foo")
	rr := httptest.NewRecorder()
	s.handleMappingDelete(rr, req)
	if rr.Code != 404 {
		t.Errorf("期望 404，得到 %d body=%s", rr.Code, rr.Body.String())
	}
}
