package main

import (
	"fmt"
	"github.com/cnstark/cc-proxy/internal/auth"
	"github.com/cnstark/cc-proxy/internal/circuitbreaker"
	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/logging"
	"github.com/cnstark/cc-proxy/internal/proxy"
	"github.com/cnstark/cc-proxy/internal/usage"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// version 在构建时通过 -ldflags 注入，默认值为 "dev"
var version = "dev"

func main() {
	// 启动阶段 logger（仅 stderr，文件 logger 需等配置加载后创建）
	bootLogger := logging.NewStdErrLogger(slog.LevelInfo)

	// --version 直接输出版本号并退出
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("ccp-proxy version", version)
		os.Exit(0)
	}

	configPath := os.Getenv("CC_PROXY_CONFIG")
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			bootLogger.Error("无法获取用户主目录", "error", err)
			os.Exit(1)
		}
		configPath = filepath.Join(home, ".cc_proxy", "config.yaml")
	}

	// 确保配置文件存在，不存在则自动创建默认配置
	if key, err := config.EnsureConfig(configPath); err != nil {
		bootLogger.Error("创建配置文件失败", "error", err)
		os.Exit(1)
	} else if key != "" {
		bootLogger.Info("已创建默认配置文件", "path", configPath, "key", logging.MaskKey(key))
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		bootLogger.Error("无法创建配置目录", "error", err, "path", filepath.Dir(configPath))
		os.Exit(1)
	}

	watcher := config.NewWatcher(configPath, 2*time.Second, bootLogger)
	defer watcher.Stop()

	snap, err := watcher.Current()
	if err != nil {
		bootLogger.Error("加载配置失败", "error", err)
		fmt.Fprintln(os.Stderr, "请先使用 ccp 命令完善配置：")
		fmt.Fprintln(os.Stderr, "  1. ccp upstream add <name> --url ... --apikey ... --model ...")
		fmt.Fprintln(os.Stderr, "  2. ccp mapping add default <请求模型名> <上游名>")
		fmt.Fprintln(os.Stderr, "  3. ccp proxy restart")
		os.Exit(1)
	}

	// CC_PROXY_LISTEN 环境变量覆盖配置文件中的 listen 地址。
	// Docker 部署时需要设为 0.0.0.0:8787，因为容器内 127.0.0.1 不接收
	// docker-proxy 通过 veth 网桥发来的连接。
	if envListen := os.Getenv("CC_PROXY_LISTEN"); envListen != "" {
		snap.Server.Listen = envListen
	}

	// 创建请求链路 logger（双写：文件 JSON + stderr Text）
	// 注意：log_level / log_file / log_max_days 仅在启动时读取，不支持热重载，
	// 修改后需重启 ccp-proxy 生效（与设计文档一致）。
	serverLevel := logging.ParseLevel(string(snap.Server.LogLevel), slog.LevelInfo)
	logger, closer, err := logging.NewLogger(serverLevel, snap.Server.LogFile, *snap.Server.LogMaxDays)
	if err != nil {
		bootLogger.Error("创建日志文件失败", "error", err)
		os.Exit(1)
	}
	defer closer.Close() // 优雅退出时关闭轮转文件

	// 将 watcher 的日志器从 stderr-only bootLogger 切换为双写 logger，
	// 使后续配置重载失败等告警同时落盘文件。
	watcher.SetLogger(logger)

	// usage tracker：进程级单例，加载历史 usage.json 并启动后台刷盘。
	// usage_stats 关闭时仍创建（保留历史数据、随时可热重载开启），仅不产生新记录。
	usagePath := filepath.Join(filepath.Dir(configPath), "usage.json")
	tracker := usage.NewTracker(usagePath)
	defer tracker.Close() // 优雅退出时 final flush

	authStore := auth.NewStore(snap.Server.PrivateKeys)
	fwd := proxy.NewStreamingForwarder()
	breaker := circuitbreaker.NewBreaker()

	handler := proxy.NewReloadingHandler(authStore, fwd, watcher, tracker, breaker, logger)

	srv := proxy.NewServer(watcher, handler, logger)
	if err := srv.Start(snap.Server.Listen); err != nil {
		bootLogger.Error("服务器错误", "error", err)
		os.Exit(1)
	}
}
