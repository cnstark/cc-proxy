package config

import "time"

// Server 代理服务器配置。
// LogLevel / LogFile / LogMaxDays / UsageStats 仅在启动时读取，不支持热重载，修改后需重启 ccp-proxy 生效。
// AdminListen / RequestLogEnabled / RequestLogMaxDays 同样启动时读取（后台端口与请求日志库路径不热重载）。
type Server struct {
	Listen              string            `yaml:"listen"`
	LogLevel            LogLevel          `yaml:"log_level"`              // 守护进程日志级别，默认 info
	LogFile             string            `yaml:"log_file"`               // 自定义日志文件路径，空=默认路径
	LogMaxDays          *int              `yaml:"log_max_days"`           // 日志保留天数，nil=默认7，0=永久保留
	UsageStats          bool              `yaml:"usage_stats"`
	PrivateKeys         map[string]string `yaml:"private_keys"`            // key → project name
	AdminListen         string            `yaml:"admin_listen"`           // 后台监听地址，默认 127.0.0.1:8788
	RequestLogEnabled   *bool             `yaml:"requestlog_enabled"`     // nil/true=开启请求日志
	RequestLogMaxDays   *int              `yaml:"requestlog_max_days"`    // nil=30，0=永久保留
}

// Upstream 上游 API 配置（cfg）
type Upstream struct {
	Name         string          `yaml:"name"`
	URL          string          `yaml:"url"`
	APIKey       string          `yaml:"apikey"`
	Models       []string        `yaml:"models"` // 该上游可服务的真实模型名列表（至少一个）
	Timeout      time.Duration   `yaml:"timeout"`
	RetryBackoff []time.Duration `yaml:"retry_backoff"` // 可选，最多 4 档退避时间；nil/空 = 关闭断路器
}

// LogLevel 日志级别
type LogLevel string

const (
	LogOff   LogLevel = "off"
	LogMeta  LogLevel = "meta"
	LogInfo  LogLevel = "info"
	LogDebug LogLevel = "debug"
)

// Project 项目配置
type Project struct {
	Name              string              `yaml:"name"`
	LogLevel          LogLevel            `yaml:"log_level"`
	ModelMap          map[string][]string `yaml:"model_map"`           // 请求模型名 → 有序 cfg 名列表
	AllowDirectAccess bool                `yaml:"allow_direct_access"` // 允许用 upstream.name 直接访问（默认 false）
}

// Config 完整配置（对应 config.yaml）
type Config struct {
	Server    Server     `yaml:"server"`
	Upstreams []Upstream `yaml:"upstreams"`
	Projects  []Project  `yaml:"projects"`
}

// ConfigSnapshot 运行时快照，包含索引后的快速查找表
type ConfigSnapshot struct {
	Server         Server
	Upstreams      map[string]Upstream // name → Upstream
	Projects       map[string]Project  // name → Project
	ModelUpstreams map[string][]string // 真实模型名 → 有序 upstream 名（按配置顺序），供直接访问 O(1) 查表
	Raw            Config              // 原始配置用于序列化
}

// NewSnapshot 从 Config 构造索引后的快照
func NewSnapshot(cfg Config) ConfigSnapshot {
	us := make(map[string]Upstream, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		us[u.Name] = u
	}
	// project.log_level 默认值 + meta→info 映射（必须在构建 ps map 之前，否则 ps 中的值不一致）
	for i := range cfg.Projects {
		if cfg.Projects[i].LogLevel == "" {
			cfg.Projects[i].LogLevel = LogOff
		}
		// 向后兼容：meta 映射为 info
		if cfg.Projects[i].LogLevel == LogMeta {
			cfg.Projects[i].LogLevel = LogInfo
		}
	}
	ps := make(map[string]Project, len(cfg.Projects))
	for _, p := range cfg.Projects {
		ps[p.Name] = p
	}
	// 默认 listen
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:8787"
	}
	// 默认 log_level
	if cfg.Server.LogLevel == "" {
		cfg.Server.LogLevel = LogInfo
	}
	// 默认 log_max_days（nil = 未设置 → 7；0 = 用户显式设为永久）
	if cfg.Server.LogMaxDays == nil {
		d := 7
		cfg.Server.LogMaxDays = &d
	}
	// 默认 admin_listen
	if cfg.Server.AdminListen == "" {
		cfg.Server.AdminListen = "127.0.0.1:8788"
	}
	// 默认 requestlog_enabled = true
	if cfg.Server.RequestLogEnabled == nil {
		t := true
		cfg.Server.RequestLogEnabled = &t
	}
	// 默认 requestlog_max_days = 30（nil=30；0=永久）
	if cfg.Server.RequestLogMaxDays == nil {
		d := 30
		cfg.Server.RequestLogMaxDays = &d
	}
	// model → 有序 upstream 名反查表（按 upstream 在配置里的出现顺序）
	modelUps := make(map[string][]string)
	for _, u := range cfg.Upstreams {
		for _, m := range u.Models {
			modelUps[m] = append(modelUps[m], u.Name)
		}
	}
	return ConfigSnapshot{
		Server:         cfg.Server,
		Upstreams:      us,
		Projects:       ps,
		ModelUpstreams: modelUps,
		Raw:            cfg,
	}
}
