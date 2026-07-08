package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// NewStdErrLogger 创建仅输出到 stderr 的 TextHandler logger，用于启动阶段。
func NewStdErrLogger(level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// NewLogger 创建双写 logger（文件 JSON + stderr Text）。
// logFile 为空时使用 DefaultLogDir()/ccp-proxy.log。
// 返回的 io.Closer 用于关闭底层轮转文件，调用方应在退出时 defer Close。
func NewLogger(level slog.Leveler, logFile string, maxDays int) (*slog.Logger, io.Closer, error) {
	if logFile == "" {
		logFile = filepath.Join(DefaultLogDir(), "ccp-proxy.log")
	}
	dir := filepath.Dir(logFile)
	baseName := filepath.Base(logFile)

	fileWriter, err := NewDailyRotateWriter(dir, baseName, maxDays)
	if err != nil {
		return nil, nil, err
	}

	jsonHandler := slog.NewJSONHandler(fileWriter, &slog.HandlerOptions{Level: level})
	textHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	dualHandler := NewDualHandler(jsonHandler, textHandler)

	return slog.New(dualHandler), fileWriter, nil
}

// ParseLevel 将配置字符串转换为 slog.Level。无效值返回 defaultLevel。
func ParseLevel(s string, defaultLevel slog.Level) slog.Level {
	switch s {
	case "off":
		return slog.LevelError + 1
	case "info", "meta":
		return slog.LevelInfo
	case "debug":
		return slog.LevelDebug
	default:
		return defaultLevel
	}
}

// DefaultLogDir 返回默认日志目录 ~/.cc_proxy/logs/。
func DefaultLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "logs")
	}
	return filepath.Join(home, ".cc_proxy", "logs")
}

// NewNopLogger 创建丢弃所有输出的 logger，用于测试。
func NewNopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// MaskKey 脱敏 API key：保留前 8 字符 + "..." + 后 4 字符（长 key）。
// 短 key（<=4）整段脱敏为 "..."，5–12 字符保留前 4。
// 统一为各调用点（proxy/config）的脱敏实现，行为取各旧实现中"更严格"的版本，
// 保证合并后不会比任一旧实现暴露更多 key 字符。
func MaskKey(key string) string {
	n := len(key)
	if n <= 4 {
		return "..."
	}
	if n <= 12 {
		return key[:4] + "..."
	}
	return key[:8] + "..." + key[n-4:]
}
