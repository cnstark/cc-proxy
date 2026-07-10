package admin

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword("correct horse", hash) {
		t.Errorf("正确密码应通过")
	}
	if VerifyPassword("wrong", hash) {
		t.Errorf("错误密码应拒绝")
	}
}

func TestSessionIssueAndVerify(t *testing.T) {
	sm := NewSessionManager("test-secret-32-bytes-long-aaaaaa")
	token, exp := sm.Issue()
	if !sm.Verify(token) {
		t.Errorf("有效 token 应通过")
	}
	if !exp.After(time.Now()) {
		t.Errorf("过期时间应在未来")
	}
	if sm.Verify("garbage") {
		t.Errorf("垃圾 token 应拒绝")
	}
	if sm.Verify(token + "x") {
		t.Errorf("篡改 token 应拒绝")
	}
}

func TestSessionExpires(t *testing.T) {
	sm := NewSessionManager("test-secret-32-bytes-long-aaaaaa")
	token, _ := sm.issueAt(time.Now().Add(-time.Hour))
	if sm.Verify(token) {
		t.Errorf("过期 token 应拒绝")
	}
}

func TestVerifyPasswordRejectsMalformedHashWithoutPanic(t *testing.T) {
	// 非数字参数（如 m=abc）不应 panic，应返回 false。
	malformed := "$argon2id$v=19$m=abc,t=2,p=4$c2FsdA$aGFzaA"
	if VerifyPassword("anything", malformed) {
		t.Errorf("畸形哈希应返回 false")
	}
	// 零参数哈希不应 panic，应返回 false。
	zeroParams := "$argon2id$v=19$m=0,t=0,p=0$c2FsdA$aGFzaA"
	if VerifyPassword("anything", zeroParams) {
		t.Errorf("零参数哈希应返回 false")
	}
}

func TestSaveLoadAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	ac := AdminConfig{PasswordHash: "$argon2id$x", SessionSecret: "deadbeef", Enabled: true}
	if err := SaveAdmin(path, ac); err != nil {
		t.Fatalf("SaveAdmin: %v", err)
	}
	loaded, err := LoadAdmin(path)
	if err != nil {
		t.Fatalf("LoadAdmin: %v", err)
	}
	if loaded.PasswordHash != ac.PasswordHash || loaded.SessionSecret != ac.SessionSecret || !loaded.Enabled {
		t.Errorf("往返不一致: %+v", loaded)
	}
}
