package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cnstark/claude-switch/internal/config"
	"github.com/cnstark/claude-switch/internal/logging"
	"github.com/cnstark/claude-switch/internal/usage"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var configPath string

// version 在构建时通过 -ldflags 注入，默认值为 "dev"
var version = "dev"

func main() {
	home, _ := os.UserHomeDir()
	defaultConfig := home + "/.claude_switch/config.yaml"

	rootCmd := &cobra.Command{
		Use:   "cs",
		Short: "claude-switch 管理工具",
		Long:  "管理 claude-switch 本地反向代理的配置：upstream、project、model mapping。",
	}
	rootCmd.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "配置文件路径")

	// === version ===
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "打印版本号",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("cs version", version)
			return nil
		},
	})

	// === key ===
	keyCmd := &cobra.Command{Use: "key", Short: "私有 key 管理"}
	keyGenCmd := &cobra.Command{
		Use:   "gen",
		Short: "生成随机私有 key（sk-cs-...）",
		RunE: func(cmd *cobra.Command, args []string) error {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return fmt.Errorf("生成随机数失败: %w", err)
			}
			fmt.Println("sk-cs-" + hex.EncodeToString(b))
			return nil
		},
	}
	keyCmd.AddCommand(keyGenCmd)
	rootCmd.AddCommand(keyCmd)

	// === upstream ===
	upstreamCmd := &cobra.Command{Use: "upstream", Short: "上游配置管理"}

	upstreamAddCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "添加上游配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("缺少 cfg 名称")
			}
			name := args[0]
			url, _ := cmd.Flags().GetString("url")
			apikey, _ := cmd.Flags().GetString("apikey")
			models, _ := cmd.Flags().GetStringSlice("model")
			timeout, _ := cmd.Flags().GetDuration("timeout")

			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			for _, u := range cfg.Upstreams {
				if u.Name == name {
					return fmt.Errorf("cfg 名 %q 已存在", name)
				}
			}
			cfg.Upstreams = append(cfg.Upstreams, config.Upstream{
				Name: name, URL: url, APIKey: apikey, Models: models, Timeout: timeout,
			})
			if err := config.Validate(cfg); err != nil {
				return err
			}
			return config.Save(cfg, configPath)
		},
	}
	upstreamAddCmd.Flags().String("url", "", "上游 API URL（必填）")
	upstreamAddCmd.Flags().String("apikey", "", "上游 API key（必填）")
	upstreamAddCmd.Flags().StringSlice("model", nil, "上游真实模型名（必填，可多次指定 --model）")
	upstreamAddCmd.Flags().Duration("timeout", 60*time.Second, "请求超时")
	upstreamAddCmd.MarkFlagRequired("url")
	upstreamAddCmd.MarkFlagRequired("apikey")
	upstreamAddCmd.MarkFlagRequired("model")

	upstreamListCmd := &cobra.Command{
		Use:   "list",
		Short: "列出所有上游配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.Upstreams) == 0 {
				fmt.Println("（无上游配置）")
				return nil
			}
			for _, u := range cfg.Upstreams {
				fmt.Printf("%-10s  %-40s  %-20s  %s\n", u.Name, u.URL, strings.Join(u.Models, ", "), u.Timeout)
			}
			return nil
		},
	}

	upstreamRemoveCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "删除上游配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("缺少 cfg 名称")
			}
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			found := false
			newList := make([]config.Upstream, 0, len(cfg.Upstreams))
			for _, u := range cfg.Upstreams {
				if u.Name == name {
					found = true
					continue
				}
				newList = append(newList, u)
			}
			if !found {
				return fmt.Errorf("cfg %q 不存在", name)
			}
			cfg.Upstreams = newList
			if err := config.Validate(cfg); err != nil {
				return err
			}
			return config.Save(cfg, configPath)
		},
	}

	upstreamUpdateCmd := &cobra.Command{
		Use:   "update <name>",
		Short: "更新上游配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("缺少 cfg 名称")
			}
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			found := false
			for i, u := range cfg.Upstreams {
				if u.Name == name {
					found = true
					if v := cmd.Flags().Lookup("url"); v != nil && v.Changed {
						cfg.Upstreams[i].URL = v.Value.String()
					}
					if v := cmd.Flags().Lookup("apikey"); v != nil && v.Changed {
						cfg.Upstreams[i].APIKey = v.Value.String()
					}
					if v := cmd.Flags().Lookup("model"); v != nil && v.Changed {
						cfg.Upstreams[i].Models, _ = cmd.Flags().GetStringSlice("model")
					}
					if cmd.Flags().Changed("timeout") {
						cfg.Upstreams[i].Timeout, _ = cmd.Flags().GetDuration("timeout")
					}
					break
				}
			}
			if !found {
				return fmt.Errorf("cfg %q 不存在", name)
			}
			if err := config.Validate(cfg); err != nil {
				return err
			}
			return config.Save(cfg, configPath)
		},
	}
	upstreamUpdateCmd.Flags().String("url", "", "新 URL")
	upstreamUpdateCmd.Flags().String("apikey", "", "新 API key")
	upstreamUpdateCmd.Flags().StringSlice("model", nil, "新模型名列表（整体替换，可多次指定 --model）")
	upstreamUpdateCmd.Flags().Duration("timeout", 0, "新超时")

	upstreamCmd.AddCommand(upstreamAddCmd, upstreamListCmd, upstreamRemoveCmd, upstreamUpdateCmd)
	rootCmd.AddCommand(upstreamCmd)

	// === project ===
	projectCmd := &cobra.Command{Use: "project", Short: "项目管理"}

	projectAddCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "添加项目",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("缺少项目名")
			}
			name := args[0]
			key, _ := cmd.Flags().GetString("key")
			logLevel, _ := cmd.Flags().GetString("log-level")

			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			for _, p := range cfg.Projects {
				if p.Name == name {
					return fmt.Errorf("项目 %q 已存在", name)
				}
			}
			if cfg.Server.PrivateKeys == nil {
				cfg.Server.PrivateKeys = make(map[string]string)
			}
			if _, exists := cfg.Server.PrivateKeys[key]; exists {
				return fmt.Errorf("私有 key 已被使用: %s", logging.MaskKey(key))
			}
			cfg.Server.PrivateKeys[key] = name
			cfg.Projects = append(cfg.Projects, config.Project{
				Name:     name,
				LogLevel: config.LogLevel(logLevel),
				ModelMap: make(map[string][]string),
			})
			if err := config.Validate(cfg); err != nil {
				return err
			}
			return config.Save(cfg, configPath)
		},
	}
	projectAddCmd.Flags().String("key", "", "私有 key（必填，cs key gen 生成）")
	projectAddCmd.Flags().String("log-level", "off", "日志级别：off, meta, debug")
	projectAddCmd.MarkFlagRequired("key")

	projectListCmd := &cobra.Command{
		Use:   "list",
		Short: "列出所有项目",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.Projects) == 0 {
				fmt.Println("（无项目）")
				return nil
			}
			for _, p := range cfg.Projects {
				fmt.Printf("%-15s  log_level=%-5s  models=%d  direct_access=%v\n", p.Name, p.LogLevel, len(p.ModelMap), p.AllowDirectAccess)
			}
			return nil
		},
	}

	projectRemoveCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "删除项目",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("缺少项目名")
			}
			name := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			found := false
			newProjects := make([]config.Project, 0, len(cfg.Projects))
			for _, p := range cfg.Projects {
				if p.Name == name {
					found = true
					continue
				}
				newProjects = append(newProjects, p)
			}
			if !found {
				return fmt.Errorf("项目 %q 不存在", name)
			}
			cfg.Projects = newProjects
			for k, v := range cfg.Server.PrivateKeys {
				if v == name {
					delete(cfg.Server.PrivateKeys, k)
				}
			}
			if len(cfg.Server.PrivateKeys) > 0 {
				if err := config.Validate(cfg); err != nil {
					return err
				}
			}
			return config.Save(cfg, configPath)
		},
	}

	projectDirectAccessCmd := &cobra.Command{
		Use:   "direct-access <name> <on|off>",
		Short: "开启或关闭项目的 allow_direct_access（用 upstream.name 直接访问）",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("用法: cs project direct-access <name> <on|off>")
			}
			name := args[0]
			var enable bool
			switch args[1] {
			case "on":
				enable = true
			case "off":
				enable = false
			default:
				return fmt.Errorf("参数必须是 on 或 off，当前 %q", args[1])
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			found := false
			for i, p := range cfg.Projects {
				if p.Name == name {
					found = true
					cfg.Projects[i].AllowDirectAccess = enable
					break
				}
			}
			if !found {
				return fmt.Errorf("项目 %q 不存在", name)
			}
			if err := config.Validate(cfg); err != nil {
				return err
			}
			return config.Save(cfg, configPath)
		},
	}

	projectCmd.AddCommand(projectAddCmd, projectListCmd, projectRemoveCmd, projectDirectAccessCmd)
	rootCmd.AddCommand(projectCmd)

	// === mapping ===
	mappingCmd := &cobra.Command{Use: "mapping", Short: "模型映射管理"}

	mappingAddCmd := &cobra.Command{
		Use:   "add <project> <request-model> <upstream/model>",
		Short: "添加模型映射",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 3 {
				return fmt.Errorf("用法: cs mapping add <project> <request-model> <upstream/model> [--backup <upstream/model>]...")
			}
			projName, reqModel, primaryTarget := args[0], args[1], args[2]
			backups, _ := cmd.Flags().GetStringSlice("backup")
			cfgList := append([]string{primaryTarget}, backups...)

			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			found := false
			for i, p := range cfg.Projects {
				if p.Name == projName {
					found = true
					if cfg.Projects[i].ModelMap == nil {
						cfg.Projects[i].ModelMap = make(map[string][]string)
					}
					if _, exists := cfg.Projects[i].ModelMap[reqModel]; exists {
						return fmt.Errorf("项目 %q 中模型 %q 已存在映射", projName, reqModel)
					}
					cfg.Projects[i].ModelMap[reqModel] = cfgList
					break
				}
			}
			if !found {
				return fmt.Errorf("项目 %q 不存在", projName)
			}
			if err := config.Validate(cfg); err != nil {
				return err
			}
			return config.Save(cfg, configPath)
		},
	}
	mappingAddCmd.Flags().StringSlice("backup", nil, "备用 upstream/model（可多次指定）")

	mappingListCmd := &cobra.Command{
		Use:   "list <project>",
		Short: "列出项目的模型映射",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("缺少项目名")
			}
			projName := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			for _, p := range cfg.Projects {
				if p.Name == projName {
					if len(p.ModelMap) == 0 {
						fmt.Println("（无映射）")
						return nil
					}
					for reqModel, cfgs := range p.ModelMap {
						fmt.Printf("%-15s  →  %s\n", reqModel, cfgs)
					}
					return nil
				}
			}
			return fmt.Errorf("项目 %q 不存在", projName)
		},
	}

	mappingRemoveCmd := &cobra.Command{
		Use:   "remove <project> <request-model>",
		Short: "删除模型映射",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("用法: cs mapping remove <project> <request-model>")
			}
			projName, reqModel := args[0], args[1]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			found := false
			for i, p := range cfg.Projects {
				if p.Name == projName {
					if _, exists := p.ModelMap[reqModel]; !exists {
						return fmt.Errorf("项目 %q 中模型 %q 不存在", projName, reqModel)
					}
					delete(cfg.Projects[i].ModelMap, reqModel)
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("项目 %q 不存在", projName)
			}
			if err := config.Validate(cfg); err != nil {
				return err
			}
			return config.Save(cfg, configPath)
		},
	}

	mappingCmd.AddCommand(mappingAddCmd, mappingListCmd, mappingRemoveCmd)
	rootCmd.AddCommand(mappingCmd)

	// === proxy ===
	proxyCmd := &cobra.Command{Use: "proxy", Short: "代理进程管理"}

	proxyStartCmd := &cobra.Command{
		Use:   "start",
		Short: "后台启动代理守护进程",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidFile := getPIDFilePath()
			if pid, err := readPID(pidFile); err == nil && processRunning(pid) {
				return fmt.Errorf("cs-proxy 已在运行 (PID: %d)", pid)
			}

			execPath, _ := os.Executable()
			proxyPath := filepath.Dir(execPath) + "/cs-proxy"
			if _, err := os.Stat(proxyPath); errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("找不到 cs-proxy 二进制: %s（请先 go build ./cmd/cs-proxy）", proxyPath)
			}

			logFile := getLogFilePath()
			os.MkdirAll(filepath.Dir(logFile), 0700)
			f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return fmt.Errorf("无法打开日志文件: %w", err)
			}

			procAttr := &os.ProcAttr{
				Dir:   filepath.Dir(proxyPath),
				Files: []*os.File{nil, f, f},
				Env:   append(os.Environ(), "CS_CONFIG="+configPath),
			}
			p, err := os.StartProcess(proxyPath, []string{"cs-proxy"}, procAttr)
			f.Close()
			if err != nil {
				return fmt.Errorf("启动 cs-proxy 失败: %w", err)
			}

			if err := writePID(pidFile, p.Pid); err != nil {
				return fmt.Errorf("写入 PID 文件失败: %w", err)
			}

			fmt.Printf("cs-proxy 已启动 (PID: %d)\n", p.Pid)
			fmt.Printf("日志: %s\n", logFile)
			return nil
		},
	}

	proxyStopCmd := &cobra.Command{
		Use:   "stop",
		Short: "停止代理守护进程",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidFile := getPIDFilePath()
			pid, err := readPID(pidFile)
			if err != nil {
				return fmt.Errorf("cs-proxy 未在运行（找不到 PID 文件）")
			}
			if !processRunning(pid) {
				os.Remove(pidFile)
				return fmt.Errorf("cs-proxy (PID: %d) 进程已不存在", pid)
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("找不到进程 (PID: %d): %w", pid, err)
			}
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("发送 SIGTERM 失败: %w", err)
			}
			fmt.Printf("已向 cs-proxy (PID: %d) 发送停止信号\n", pid)
			os.Remove(pidFile)
			return nil
		},
	}

	proxyStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "检查代理是否运行",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidFile := getPIDFilePath()
			pid, err := readPID(pidFile)
			if err != nil {
				fmt.Println("cs-proxy 未运行")
				return nil
			}
			if !processRunning(pid) {
				fmt.Println("cs-proxy 未运行（PID 文件过期）")
				os.Remove(pidFile)
				return nil
			}
			fmt.Printf("cs-proxy 运行中 (PID: %d) ✓\n", pid)
			return nil
		},
	}

	proxyLogsCmd := &cobra.Command{
		Use:   "logs",
		Short: "查看代理日志",
		RunE: func(cmd *cobra.Command, args []string) error {
			logFile := getLogFilePath()
			data, err := os.ReadFile(logFile)
			if err != nil {
				return fmt.Errorf("无法读取日志文件 %s: %w", logFile, err)
			}
			projFilter, _ := cmd.Flags().GetString("project")
			levelFilter, _ := cmd.Flags().GetString("level")

			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) != nil {
					fmt.Println(line)
					continue
				}
				if projFilter != "" && entry["project"] != projFilter {
					continue
				}
				if levelFilter == "debug" {
					fmt.Println(line)
				} else {
					delete(entry, "request_body")
					delete(entry, "response_body")
					b, _ := json.Marshal(entry)
					fmt.Println(string(b))
				}
			}
			return nil
		},
	}
	proxyLogsCmd.Flags().String("project", "", "按项目名筛选")
	proxyLogsCmd.Flags().String("level", "", "显示级别：debug 显示完整请求/响应体")

	proxyCmd.AddCommand(proxyStartCmd, proxyStopCmd, proxyStatusCmd, proxyLogsCmd)
	rootCmd.AddCommand(proxyCmd)

	// === stats ===
	statsCmd := &cobra.Command{
		Use:   "stats [project]",
		Short: "查看 token 用量统计",
		Long:  "读取 ~/.claude_switch/usage.json，按 project/model/date 汇总 token 用量（input/output/cache_creation/cache_read）。",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := ""
			if len(args) > 0 {
				project = args[0]
			}
			since, _ := cmd.Flags().GetString("since")
			model, _ := cmd.Flags().GetString("model")
			usagePath := filepath.Join(filepath.Dir(configPath), "usage.json")
			out, err := usage.RunStats(usagePath, project, since, model)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	statsCmd.Flags().String("since", "7d", "时间区间：1d/7d/30d 或 YYYY-MM-DD")
	statsCmd.Flags().String("model", "", "按模型过滤")
	rootCmd.AddCommand(statsCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// === helpers ===

func loadConfig() (config.Config, error) {
	snap, err := config.LoadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 配置文件不存在，自动创建默认配置
			key, createErr := config.EnsureConfig(configPath)
			if createErr != nil {
				return config.Config{}, fmt.Errorf("自动创建配置文件失败: %w", createErr)
			}
			if key != "" {
				fmt.Fprintf(os.Stderr, "已创建默认配置文件: %s\n", configPath)
				fmt.Fprintf(os.Stderr, "默认私有 key: %s\n", key)
				fmt.Fprintf(os.Stderr, "请使用 cs 命令添加上游和映射后，用 cs proxy start 启动代理\n\n")
			}
			// 重新加载
			snap, err = config.LoadFile(configPath)
			if err != nil {
				return config.Config{}, fmt.Errorf("加载配置文件失败: %w", err)
			}
			return snap.Raw, nil
		}
		// 校验失败时尝试读取原始 YAML（允许不完整配置进行只读操作）
		if raw, e := os.ReadFile(configPath); e == nil {
			var cfg config.Config
			if e := yaml.Unmarshal(raw, &cfg); e == nil {
				return cfg, nil
			}
		}
		return config.Config{}, fmt.Errorf("加载配置失败: %w", err)
	}
	return snap.Raw, nil
}

func getPIDFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude_switch", "cs-proxy.pid")
}

func getLogFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude_switch", "cs-proxy.log")
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func writePID(path string, pid int) error {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0700)
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0600)
}

func processRunning(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
