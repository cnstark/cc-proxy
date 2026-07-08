package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewNopLogger_DiscardsAll(t *testing.T) {
	logger := NewNopLogger()
	logger.Info("should not appear anywhere")
	// 不 panic 即通过
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"off", slog.LevelError + 1},
		{"info", slog.LevelInfo},
		{"meta", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"invalid", slog.LevelInfo}, // default
		{"", slog.LevelInfo},
	}
	for _, tc := range tests {
		got := ParseLevel(tc.input, slog.LevelInfo)
		if got != tc.expected {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func TestDailyRotateWriter_WriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	w, err := NewDailyRotateWriter(dir, "test.log", 7)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	defer w.Close()

	// 写入
	_, err = w.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// 验证文件存在
	path := filepath.Join(dir, "test.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}

	// 模拟跨天：手动调用 rotate
	w.mu.Lock()
	err = w.rotate("2000-01-01")
	w.mu.Unlock()
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// 验证历史文件存在
	today := time.Now().Format("2006-01-02")
	datedPath := filepath.Join(dir, "test-"+today+".log")
	if _, err := os.Stat(datedPath); os.IsNotExist(err) {
		t.Fatalf("expected dated file %s to exist", datedPath)
	}
}

func TestDailyRotateWriter_Cleanup(t *testing.T) {
	dir := t.TempDir()
	// 创建过期文件
	oldDate := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	oldPath := filepath.Join(dir, "test-"+oldDate+".log")
	os.WriteFile(oldPath, []byte("old"), 0600)

	// 创建近期文件
	recentDate := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	recentPath := filepath.Join(dir, "test-"+recentDate+".log")
	os.WriteFile(recentPath, []byte("recent"), 0600)

	w, err := NewDailyRotateWriter(dir, "test.log", 7)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	defer w.Close()

	// 触发清理
	w.mu.Lock()
	w.cleanup()
	w.mu.Unlock()

	// 过期文件应被删除
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("expected old file to be deleted")
	}
	// 近期文件应保留
	if _, err := os.Stat(recentPath); os.IsNotExist(err) {
		t.Fatal("expected recent file to be kept")
	}
}

func TestDailyRotateWriter_MaxDaysZero_NoCleanup(t *testing.T) {
	dir := t.TempDir()
	oldDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	oldPath := filepath.Join(dir, "test-"+oldDate+".log")
	os.WriteFile(oldPath, []byte("old"), 0600)

	w, err := NewDailyRotateWriter(dir, "test.log", 0)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	defer w.Close()

	w.mu.Lock()
	w.cleanup()
	w.mu.Unlock()

	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		t.Fatal("expected old file to be kept when maxDays=0")
	}
}

func TestDualHandler_BothWritten(t *testing.T) {
	var bufA, bufB bytes.Buffer
	a := slog.NewJSONHandler(&bufA, &slog.HandlerOptions{Level: slog.LevelDebug})
	b := slog.NewJSONHandler(&bufB, &slog.HandlerOptions{Level: slog.LevelDebug})
	dual := NewDualHandler(a, b)
	logger := slog.New(dual)

	logger.Info("test")

	if !strings.Contains(bufA.String(), "test") {
		t.Fatal("expected handler A to receive log")
	}
	if !strings.Contains(bufB.String(), "test") {
		t.Fatal("expected handler B to receive log")
	}
}

func TestNewStdErrLogger(t *testing.T) {
	logger := NewStdErrLogger(slog.LevelInfo)
	logger.Info("stderr test")
	// 不 panic 即通过
}

// TestNewLogger 验证 NewLogger 工厂装配：文件 sink 为 JSON，且级别过滤经工厂生效。
// 不替换 os.Stderr（与并行测试包冲突），stderr 侧由 TestNewStdErrLogger 覆盖。
func TestNewLogger_LevelEnforcementAndJSONSink(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	logger, closer, err := NewLogger(slog.LevelInfo, logFile, 7)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closer.Close()

	logger.Info("hello-from-test")
	logger.Debug("should-be-suppressed")

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	// Info 行应落盘
	if !strings.Contains(content, "hello-from-test") {
		t.Errorf("expected log file to contain Info message, got: %q", content)
	}
	// Debug 行应被级别过滤（LevelInfo 下 Debug 不 Enabled，不会写入）
	if strings.Contains(content, "should-be-suppressed") {
		t.Errorf("expected Debug message to be suppressed, got: %q", content)
	}

	// 解析 JSON 行验证 msg 字段（slog.NewJSONHandler 默认消息键为 "msg"）
	var foundMsg bool
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("unmarshal log line: %v: %q", err, line)
			continue
		}
		if msg, _ := m["msg"].(string); msg == "hello-from-test" {
			foundMsg = true
		}
	}
	if !foundMsg {
		t.Errorf("expected a JSON log entry with msg=%q, got: %q", "hello-from-test", content)
	}
}

// TestNewLogger_EmptyLogFileFallback 验证 logFile 为空时回退到默认目录且不报错。
// 通过 t.Setenv 将用户主目录重定向到临时目录，避免污染真实 ~/.cc_proxy/logs。
func TestNewLogger_EmptyLogFileFallback(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir: Windows 读 USERPROFILE，POSIX 读 HOME，同时设置以兼容双平台
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	logger, closer, err := NewLogger(slog.LevelInfo, "", 7)
	if err != nil {
		t.Fatalf("NewLogger with empty logFile: %v", err)
	}
	defer closer.Close()

	logger.Info("default-dir-test")

	wantPath := filepath.Join(home, ".cc_proxy", "logs", "ccp-proxy.log")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("expected log at default dir %s, read error: %v", wantPath, err)
	}
	if !strings.Contains(string(data), "default-dir-test") {
		t.Errorf("expected default dir log to contain message, got: %q", string(data))
	}
}
