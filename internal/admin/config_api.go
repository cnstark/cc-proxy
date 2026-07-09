package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/logging"
)

// isMasked 判断字符串是否为脱敏占位（含 "..."）。
func isMasked(s string) bool {
	return strings.Contains(s, "...")
}

// handleConfigGet 返回当前配置，apikey 与 private key 脱敏。
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	snap, err := config.LoadFile(s.configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, 200, config.Config{})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "加载配置失败: " + err.Error()})
		return
	}
	cfg := snap.Raw
	for i := range cfg.Upstreams {
		cfg.Upstreams[i].APIKey = logging.MaskKey(cfg.Upstreams[i].APIKey)
	}
	maskedKeys := make(map[string]string, len(cfg.Server.PrivateKeys))
	for k, v := range cfg.Server.PrivateKeys {
		maskedKeys[logging.MaskKey(k)] = v
	}
	cfg.Server.PrivateKeys = maskedKeys
	writeJSON(w, 200, cfg)
}

// handleConfigPut 整体替换配置。脱敏占位（含 "..."）的 apikey/private key 保留原值。
func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var incoming config.Config
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeJSON(w, 400, map[string]string{"error": "JSON 解析失败: " + err.Error()})
		return
	}
	orig, origErr := config.LoadFile(s.configPath)
	if origErr == nil {
		origByKey := map[string]config.Upstream{}
		for _, u := range orig.Upstreams {
			origByKey[u.Name] = u
		}
		for i, u := range incoming.Upstreams {
			if isMasked(u.APIKey) {
				if o, ok := origByKey[u.Name]; ok {
					incoming.Upstreams[i].APIKey = o.APIKey
				}
			}
		}
		if len(incoming.Server.PrivateKeys) > 0 && len(orig.Server.PrivateKeys) > 0 {
			origByProj := map[string]string{}
			for k, v := range orig.Server.PrivateKeys {
				origByProj[v] = k // project → 原始 key
			}
			fixed := make(map[string]string, len(incoming.Server.PrivateKeys))
			for maskedKey, proj := range incoming.Server.PrivateKeys {
				if isMasked(maskedKey) {
					if origKey, ok := origByProj[proj]; ok {
						fixed[origKey] = proj
						continue
					}
				}
				fixed[maskedKey] = proj
			}
			incoming.Server.PrivateKeys = fixed
		}
	}
	if err := config.Validate(incoming); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(incoming, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": "保存失败: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
