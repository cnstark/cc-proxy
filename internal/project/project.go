package project

// ResolvedTarget 路由解析出的单个目标：用哪个 upstream 转发、改写请求体为哪个真实 model。
type ResolvedTarget struct {
	Upstream string
	Model    string
}

// ProjectRoute 单个项目的路由数据。
// ModelMap 是「请求模型名别名 → 有序 ResolvedTarget 列表」（已预解析，列表顺序即主备故障转移顺序）。
// AllowDirect 开启后，请求模型名若等于某 upstream 的真实 model 名，可直接路由到所有提供该 model 的 upstream。
type ProjectRoute struct {
	ModelMap    map[string][]ResolvedTarget
	AllowDirect bool
}

// ModelResolver 按项目名 + 请求模型名查找有序目标列表。
// 别名查表优先；项目开启直连时，请求模型名命中某真实 model 名则回退直连（按全局配置顺序）。
type ModelResolver struct {
	projects       map[string]ProjectRoute
	modelUpstreams map[string][]string // 真实模型名 → 有序 upstream 名
}

// NewResolver 从配置数据创建查表器。
// projects: 项目名 → ProjectRoute（ModelMap 已预解析）；modelUpstreams: 真实模型名 → 有序 upstream 名。
func NewResolver(projects map[string]ProjectRoute, modelUpstreams map[string][]string) *ModelResolver {
	return &ModelResolver{projects: projects, modelUpstreams: modelUpstreams}
}

// Resolve 返回项目下某请求模型对应的有序目标列表。
// 1. 别名命中 model_map → 返回该列表
// 2. 项目开启直连且请求模型名是某真实 model 名 → 返回所有提供该 model 的 upstream（model=请求名）
// 否则 miss。
func (r *ModelResolver) Resolve(projectName, requestModel string) ([]ResolvedTarget, bool) {
	proj, ok := r.projects[projectName]
	if !ok {
		return nil, false
	}
	if targets, ok := proj.ModelMap[requestModel]; ok && len(targets) > 0 {
		return targets, true
	}
	if proj.AllowDirect {
		if ups, ok := r.modelUpstreams[requestModel]; ok && len(ups) > 0 {
			targets := make([]ResolvedTarget, len(ups))
			for i, up := range ups {
				targets[i] = ResolvedTarget{Upstream: up, Model: requestModel}
			}
			return targets, true
		}
	}
	return nil, false
}
