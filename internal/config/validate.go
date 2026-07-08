package config

import (
	"fmt"
	"strings"

	"github.com/cnstark/cc-proxy/internal/logging"
)

// Validate 校验 Config 的所有规则，返回第一个错误或 nil
func Validate(cfg Config) error {
	// 1. 至少配置一个 private key
	if len(cfg.Server.PrivateKeys) == 0 {
		return fmt.Errorf("server.private_keys: 至少需要配置一个私有 key")
	}

	// 2. upstream name 唯一、非空、不含 /（model_map 解析依赖 / 分隔）
	seenUpstream := make(map[string]bool)
	for _, u := range cfg.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("upstreams: cfg 名不能为空")
		}
		if strings.Contains(u.Name, "/") {
			return fmt.Errorf("upstreams.%s: name 不能包含 /（model_map 解析依赖 / 分隔）", u.Name)
		}
		if seenUpstream[u.Name] {
			return fmt.Errorf("upstreams: cfg 名 %q 重复", u.Name)
		}
		seenUpstream[u.Name] = true
	}

	// 2.1 models 非空、每项非空、无重复
	for _, u := range cfg.Upstreams {
		if len(u.Models) == 0 {
			return fmt.Errorf("upstreams.%s.models: 至少需要一个模型", u.Name)
		}
		seenModel := make(map[string]bool)
		for _, m := range u.Models {
			if m == "" {
				return fmt.Errorf("upstreams.%s.models: 模型名不能为空", u.Name)
			}
			if seenModel[m] {
				return fmt.Errorf("upstreams.%s.models: 模型 %q 重复", u.Name, m)
			}
			seenModel[m] = true
		}
	}

	// 3. project name 唯一且非空
	seenProject := make(map[string]bool)
	for _, p := range cfg.Projects {
		if p.Name == "" {
			return fmt.Errorf("projects: 项目名不能为空")
		}
		if seenProject[p.Name] {
			return fmt.Errorf("projects: 项目名 %q 重复", p.Name)
		}
		seenProject[p.Name] = true
	}

	// 4. private key 指向存在的 project
	seenKeys := make(map[string]bool)
	for key, projName := range cfg.Server.PrivateKeys {
		if seenKeys[key] {
			return fmt.Errorf("server.private_keys: key %q 重复", logging.MaskKey(key))
		}
		seenKeys[key] = true
		if !seenProject[projName] {
			return fmt.Errorf("server.private_keys: key %q 指向不存在的项目 %q", logging.MaskKey(key), projName)
		}
	}

	// 5. model_map 条目格式 upstream/model，引用的 upstream 与 model 必须存在
	upstreamModels := make(map[string]map[string]bool, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		set := make(map[string]bool, len(u.Models))
		for _, m := range u.Models {
			set[m] = true
		}
		upstreamModels[u.Name] = set
	}
	for _, p := range cfg.Projects {
		for reqModel, list := range p.ModelMap {
			if len(list) == 0 {
				return fmt.Errorf("projects.%s.model_map.%s: cfg 列表不能为空", p.Name, reqModel)
			}
			for _, entry := range list {
				up, model, ok := ParseUpstreamModel(entry)
				if !ok {
					return fmt.Errorf("projects.%s.model_map.%s: %q 格式错误，应为 upstream/model", p.Name, reqModel, entry)
				}
				models, exists := upstreamModels[up]
				if !exists {
					return fmt.Errorf("projects.%s.model_map.%s: 引用了不存在的 upstream %q", p.Name, reqModel, up)
				}
				if !models[model] {
					return fmt.Errorf("projects.%s.model_map.%s: upstream %q 不提供模型 %q", p.Name, reqModel, up, model)
				}
			}
		}
	}

	// 6. retry_backoff 校验
	for _, u := range cfg.Upstreams {
		if len(u.RetryBackoff) > 4 {
			return fmt.Errorf("upstreams.%s.retry_backoff: 最多支持 4 档退避时间，当前 %d 档", u.Name, len(u.RetryBackoff))
		}
		for i, d := range u.RetryBackoff {
			if d <= 0 {
				return fmt.Errorf("upstreams.%s.retry_backoff[%d]: 退避时间必须为正数，当前 %s", u.Name, i, d)
			}
		}
	}

	// 7. project.log_level 合法性（meta 兼容旧配置，info 为新值）
	for _, p := range cfg.Projects {
		switch p.LogLevel {
		case "", LogOff, LogMeta, LogInfo, LogDebug:
			// 合法
		default:
			return fmt.Errorf("projects.%s.log_level: 无效值 %q（允许: %s）", p.Name, p.LogLevel, strings.Join(validLogLevelsStr(true), ", "))
		}
	}

	// 8. server.log_level 合法性（server 不支持 meta）
	switch cfg.Server.LogLevel {
	case "", LogOff, LogInfo, LogDebug:
		// 合法
	default:
		return fmt.Errorf("server.log_level: 无效值 %q（允许: %s）", cfg.Server.LogLevel, strings.Join(validLogLevelsStr(false), ", "))
	}

	// 9. server.log_max_days 范围（nil 已由 NewSnapshot 填充默认值，此处仅校验 >=0）
	if cfg.Server.LogMaxDays != nil && *cfg.Server.LogMaxDays < 0 {
		return fmt.Errorf("server.log_max_days: 不能为负数，当前 %d", *cfg.Server.LogMaxDays)
	}

	return nil
}

// validLogLevelsStr 返回允许的日志级别字符串，用于校验错误信息。
// includeMeta=true 时包含 meta（project 兼容旧配置），false 时排除（server 不支持 meta）。
func validLogLevelsStr(includeMeta bool) []string {
	levels := []string{string(LogOff)}
	if includeMeta {
		levels = append(levels, string(LogMeta))
	}
	levels = append(levels, string(LogInfo), string(LogDebug))
	return levels
}
