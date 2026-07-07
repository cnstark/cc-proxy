package config

import "strings"

// ParseUpstreamModel 把 "upstream/model" 切成 (upstream, model)。
// 按第一个 / 切分：左=upstream name，右=model name（可继续含 /）。
// ok=false 表示格式非法：无 /、upstream 为空、或 model 为空。
// upstream name 不得含 / 由 Validate 保证，此处只做切分。
func ParseUpstreamModel(s string) (upstream, model string, ok bool) {
	idx := strings.Index(s, "/")
	if idx <= 0 || idx == len(s)-1 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}
