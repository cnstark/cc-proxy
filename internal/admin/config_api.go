package admin

import (
	"crypto/rand"
	"encoding/hex"
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

// loadCfg 加载当前配置（原始 YAML 只读）。
func (s *Server) loadCfg() (config.Config, error) {
	snap, err := config.LoadFile(s.configPath)
	if err != nil {
		return config.Config{}, err
	}
	return snap.Raw, nil
}

func (s *Server) handleUpstreamCreate(w http.ResponseWriter, r *http.Request) {
	var u config.Upstream
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	for _, x := range cfg.Upstreams {
		if x.Name == u.Name {
			writeJSON(w, 409, map[string]string{"error": "upstream 已存在"})
			return
		}
	}
	cfg.Upstreams = append(cfg.Upstreams, u)
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, map[string]string{"status": "ok"})
}

func (s *Server) handleUpstreamUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var u config.Upstream
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	found := false
	for i, x := range cfg.Upstreams {
		if x.Name == name {
			if isMasked(u.APIKey) {
				u.APIKey = x.APIKey
			}
			cfg.Upstreams[i] = u
			cfg.Upstreams[i].Name = name
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "upstream 不存在"})
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleUpstreamDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	out := cfg.Upstreams[:0]
	found := false
	for _, x := range cfg.Upstreams {
		if x.Name == name {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "upstream 不存在"})
		return
	}
	cfg.Upstreams = out
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Key      string `json:"key"`
		LogLevel string `json:"log_level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	for _, p := range cfg.Projects {
		if p.Name == body.Name {
			writeJSON(w, 409, map[string]string{"error": "project 已存在"})
			return
		}
	}
	if cfg.Server.PrivateKeys == nil {
		cfg.Server.PrivateKeys = map[string]string{}
	}
	cfg.Server.PrivateKeys[body.Key] = body.Name
	cfg.Projects = append(cfg.Projects, config.Project{Name: body.Name, LogLevel: config.LogLevel(body.LogLevel), ModelMap: map[string][]string{}})
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		LogLevel string `json:"log_level"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	found := false
	for i, p := range cfg.Projects {
		if p.Name == name {
			cfg.Projects[i].LogLevel = config.LogLevel(body.LogLevel)
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "project 不存在"})
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	out := cfg.Projects[:0]
	found := false
	for _, p := range cfg.Projects {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "project 不存在"})
		return
	}
	cfg.Projects = out
	for k, v := range cfg.Server.PrivateKeys {
		if v == name {
			delete(cfg.Server.PrivateKeys, k)
		}
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleDirectAccess(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		On bool `json:"on"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	found := false
	for i, p := range cfg.Projects {
		if p.Name == name {
			cfg.Projects[i].AllowDirectAccess = body.On
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "project 不存在"})
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleMappingCreate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Model   string   `json:"model"`
		Targets []string `json:"targets"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	found := false
	for i, p := range cfg.Projects {
		if p.Name == name {
			if cfg.Projects[i].ModelMap == nil {
				cfg.Projects[i].ModelMap = map[string][]string{}
			}
			cfg.Projects[i].ModelMap[body.Model] = body.Targets
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "project 不存在"})
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, map[string]string{"status": "ok"})
}

func (s *Server) handleMappingDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	model := r.PathValue("model")
	cfg, err := s.loadCfg()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	found := false
	for i, p := range cfg.Projects {
		if p.Name == name {
			delete(cfg.Projects[i].ModelMap, model)
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "project 不存在"})
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg, s.configPath); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleKeyGen(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	rand.Read(b)
	writeJSON(w, 200, map[string]string{"key": "sk-cp-" + hex.EncodeToString(b)})
}
