// Package requestlog 请求日志的结构化存储（SQLite）。
// 与代理运行日志（ccp-proxy.log）分离：本包只记录每个请求的处理元数据与（debug 级）请求/响应体。
package requestlog

// Entry 一条请求日志。
type Entry struct {
	Project      string // 命中的 project（鉴权失败时为空）
	Method       string // HTTP 方法
	Path         string // 请求路径
	Model        string // 请求模型名（别名）
	Upstream     string // 命中的真实上游名
	RealModel    string // 重写后的真实模型名
	Status       int    // 响应状态码
	DurationMs   int64  // 耗时毫秒
	Error        string // 失败原因（空=成功）
	RequestBody  string // 仅 debug 级
	ResponseBody string // 仅 debug 级
}

// Recorder 把一条请求日志落到某处。Store 满足此接口；
// 测试可用假实现替换，proxy 依赖此接口而非具体 Store。
type Recorder interface {
	Record(Entry)
}
