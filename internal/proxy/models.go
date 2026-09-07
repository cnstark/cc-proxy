package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/cnstark/cc-proxy/internal/config"
	"github.com/cnstark/cc-proxy/internal/logging"
)

// modelsObject 是 Anthropic Models API 响应中的单个模型对象。
// 字段参考 GET /v1/models/{id} 的响应格式；alias 标记由配置合成（非上游透传）的条目，
// 供日志区分，不写入响应 JSON。
type modelsObject struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
	Type        string `json:"type"`

	alias bool // true = 项目 model_map 别名（合成条目）
}

// ModelsHandler 处理 GET /v1/models（列表）与 GET /v1/models/{id}（单个）。
// 响应由配置快照合成，不透传上游：
//   - 项目未开启 allow_direct_access：只返回 model_map 配置的模型别名（Resolve 可见）；
//   - 开启后：别名 + 所有可直接访问的真实模型（含被别名映射引用的真实模型，与 Resolve 的可见性一致）。
type ModelsHandler struct {
	auth     AuthStore
	resolver ModelResolver
	lookup   ConfigLookup
	snap     *config.ConfigSnapshot
	log      *slog.Logger
}

// NewModelsHandler 创建模型列表 handler。snap 为当次请求的配置快照（热重载安全）。
func NewModelsHandler(auth AuthStore, resolver ModelResolver, lookup ConfigLookup, snap *config.ConfigSnapshot, log *slog.Logger) *ModelsHandler {
	return &ModelsHandler{auth: auth, resolver: resolver, lookup: lookup, snap: snap, log: log}
}

// ServeHTTP 分发 /v1/models 与 /v1/models/{id}。
func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "仅支持 GET")
		return
	}

	// 与 Handler 一致的鉴权顺序：优先 x-api-key，其次 Authorization: Bearer <key>
	apiKey := r.Header.Get("x-api-key")
	if apiKey == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			apiKey = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	projectName, ok := h.auth.Authenticate(apiKey)
	if !ok {
		h.log.InfoContext(r.Context(), "models auth failed", "key_prefix", logging.MaskKey(apiKey))
		writeError(w, http.StatusUnauthorized, "authentication_error", "无效的 API key")
		return
	}

	models := h.visibleModels(r, projectName)

	// 单个模型查询：/v1/models/{model_id}（model id 不允许含 "/"）
	if rest := strings.TrimPrefix(r.URL.Path, "/v1/models/"); rest != r.URL.Path {
		// 命中了 "/v1/models/" 前缀
		if rest == "" || strings.Contains(rest, "/") {
			writeError(w, http.StatusNotFound, "not_found_error", "无效的模型路径")
			return
		}
		for _, m := range models {
			if m.ID == rest {
				writeModelsJSON(w, m)
				return
			}
		}
		writeError(w, http.StatusNotFound, "not_found_error",
			"模型 "+rest+" 不存在或当前项目不可访问")
		return
	}

	// 列表查询。limit/after_id/before_id 分页参数直接忽略，返回全量列表。
	resp := map[string]any{
		"data":     models,
		"has_more": false,
	}
	if len(models) > 0 {
		resp["first_id"] = models[0].ID
		resp["last_id"] = models[len(models)-1].ID
	}
	writeModelsJSON(w, resp)
}

// visibleModels 返回当前项目可见的模型列表，稳定排序（按 id）。
func (h *ModelsHandler) visibleModels(r *http.Request, projectName string) []modelsObject {
	seen := make(map[string]bool)
	models := make([]modelsObject, 0)

	// 1. model_map 别名（无需 allow_direct_access 即对项目可见）
	if p, ok := h.snap.Projects[projectName]; ok {
		for alias := range p.ModelMap {
			// Resolve 不可见（列表为空）的别名不展示
			if _, ok := h.resolver.Resolve(projectName, alias); !ok {
				continue
			}
			models = append(models, modelsObject{
				ID:          alias,
				DisplayName: alias,
				CreatedAt:   configSnapshotCreatedAt,
				Type:        "model",
				alias:       true,
			})
			seen[alias] = true
		}
	}

	// 2. 真实模型：仅当 Resolve 对项目可见（即项目开启直连且该模型有上游可提供）。
	// 与请求转发路径的可见性判定保持一致：不开直连时绝不暴露真实模型名。
	for _, name := range h.sortedRealModels() {
		if seen[name] {
			continue
		}
		if _, ok := h.resolver.Resolve(projectName, name); !ok {
			continue
		}
		models = append(models, modelsObject{
			ID:          name,
			DisplayName: name,
			CreatedAt:   configSnapshotCreatedAt,
			Type:        "model",
		})
		seen[name] = true
	}

	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	h.log.DebugContext(r.Context(), "models list resolved",
		"project", projectName,
		"aliases", countAliases(models),
		"total", len(models),
	)
	return models
}

// sortedRealModels 返回快照中所有真实模型名（配置顺序去重后的排序结果）。
// 遍历 ModelUpstreams 的 key 即为「至少一个上游可服务」的真实模型集合。
func (h *ModelsHandler) sortedRealModels() []string {
	names := make([]string, 0, len(h.snap.ModelUpstreams))
	for name := range h.snap.ModelUpstreams {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func countAliases(models []modelsObject) int {
	n := 0
	for _, m := range models {
		if m.alias {
			n++
		}
	}
	return n
}

// configSnapshotCreatedAt 是合成模型对象的 created_at 占位值。
// 配置快照无创建时间概念，用固定 RFC3339 零点表示「配置定义，无真实创建时间」。
const configSnapshotCreatedAt = "1970-01-01T00:00:00Z"

// writeModelsJSON 写 JSON 响应（与主链路一致，不转义 <>&）。
func writeModelsJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
