package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Recorder 把一个请求的 usage 落到某处。Tracker 满足此接口；
// 测试可用假实现替换，proxy 依赖此接口而非具体 Tracker。
type Recorder interface {
	Record(project, model, date string, u TokenUsage)
}

// File usage.json 的磁盘格式
type File struct {
	Version int                                          `json:"version"`
	Buckets map[string]map[string]map[string]*TokenUsage `json:"buckets"`
}

const fileVersion = 1

// Tracker 进程级单例：内存累加 usage + 后台刷盘。满足 Recorder。
type Tracker struct {
	mu       sync.Mutex
	dirty    bool
	path     string
	data     map[string]map[string]map[string]*TokenUsage
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewTracker 加载已有 usage.json（fail-soft）并启动后台刷盘 goroutine。
func NewTracker(path string) *Tracker {
	t := &Tracker{
		path:   path,
		data:   make(map[string]map[string]map[string]*TokenUsage),
		stopCh: make(chan struct{}),
	}
	t.load()
	go t.flushLoop()
	return t
}

// Record 累加一个请求的 usage（project→model→date 桶）。O(1)，持整个 map 锁。
func (t *Tracker) Record(project, model, date string, u TokenUsage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	mm, ok := t.data[project]
	if !ok {
		mm = make(map[string]map[string]*TokenUsage)
		t.data[project] = mm
	}
	dd, ok := mm[model]
	if !ok {
		dd = make(map[string]*TokenUsage)
		mm[model] = dd
	}
	bucket, ok := dd[date]
	if !ok {
		bucket = &TokenUsage{}
		dd[date] = bucket
	}
	bucket.Input += u.Input
	bucket.Output += u.Output
	bucket.CacheCreation += u.CacheCreation
	bucket.CacheRead += u.CacheRead
	t.dirty = true
}

// Flush 若有变更则原子写盘，返回写盘错误（调用方记日志，不阻断转发）。
func (t *Tracker) Flush() error {
	t.mu.Lock()
	if !t.dirty {
		t.mu.Unlock()
		return nil
	}
	data, err := json.Marshal(File{Version: fileVersion, Buckets: t.data})
	if err != nil {
		t.mu.Unlock()
		return fmt.Errorf("序列化 usage 失败: %w", err)
	}
	t.dirty = false
	t.mu.Unlock()

	if err := t.atomicWrite(data); err != nil {
		// 写盘失败：恢复 dirty，下次重试
		t.mu.Lock()
		t.dirty = true
		t.mu.Unlock()
		return err
	}
	return nil
}

// Close 停止刷盘 goroutine 并做最终 flush。幂等。
func (t *Tracker) Close() error {
	t.stopOnce.Do(func() { close(t.stopCh) })
	return t.Flush()
}

func (t *Tracker) flushLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := t.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "[ccp-proxy] usage 刷盘失败（保留 dirty 重试）: %v\n", err)
			}
		case <-t.stopCh:
			return
		}
	}
}

// load 启动时加载。文件不存在或损坏 → 从空开始（记 stderr），不阻断启动。
// 版本不兼容 → 备份旧文件后从空开始。
func (t *Tracker) load() {
	data, err := os.ReadFile(t.path)
	if err != nil {
		return // 文件不存在，从空开始
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		fmt.Fprintf(os.Stderr, "[ccp-proxy] usage 文件解析失败（从空开始）: %v\n", err)
		return
	}
	if f.Version != fileVersion {
		backup := t.path + ".bak." + time.Now().Format("20060102-150405")
		if rerr := os.Rename(t.path, backup); rerr == nil {
			fmt.Fprintf(os.Stderr, "[ccp-proxy] usage 文件版本不兼容（%d），已备份到 %s，从空开始\n", f.Version, backup)
		} else {
			fmt.Fprintf(os.Stderr, "[ccp-proxy] usage 文件版本不兼容（%d），备份失败: %v，从空开始\n", f.Version, rerr)
		}
		return
	}
	t.data = f.Buckets
	if t.data == nil {
		t.data = make(map[string]map[string]map[string]*TokenUsage)
	}
}

func (t *Tracker) atomicWrite(data []byte) error {
	dir := filepath.Dir(t.path)
	tmp, err := os.CreateTemp(dir, ".usage-*.json.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, t.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("原子替换 usage 文件失败: %w", err)
	}
	return nil
}

// Collector 把 Recorder + project + model 绑成 per-request 句柄。
// forward.go 在响应到达后 Attach（按 content-type 选解析模式），
// 逐 chunk Feed，流结束 Close 触发一次 Record（仅当扫到 usage）。
// model 在故障转移循环中可通过 SetModel 更新为上游真实模型名。
type Collector struct {
	rec     Recorder
	project string
	model   string
	scanner *Scanner // Attach 后创建
}

// SetModel 更新统计用的模型名（用于故障转移时设为上游真实模型名）。
func (c *Collector) SetModel(model string) {
	c.model = model
}

// NewCollector 创建 per-request 收集器。rec 通常为 *Tracker，测试可传假实现。
func NewCollector(rec Recorder, project, model string) *Collector {
	return &Collector{rec: rec, project: project, model: model}
}

// Attach 按响应 content-type 决定流式/非流式解析模式。
func (c *Collector) Attach(contentType string) {
	defer func() { recover() }() // usage 任何失败都不得中断转发
	streaming := strings.Contains(contentType, "text/event-stream")
	c.scanner = NewScanner(streaming, func(u TokenUsage) {
		if c.rec != nil { // rec 为 nil（usage_stats 关或无 tracker）时只解析供日志，不落盘
			c.rec.Record(c.project, c.model, today(), u)
		}
	})
}

// Feed 喂入响应 chunk（旁路，在写给客户端之后调用，不增加客户端延迟）。
func (c *Collector) Feed(chunk []byte) {
	defer func() { recover() }()
	if c.scanner != nil {
		c.scanner.Feed(chunk)
	}
}

// Close 流结束，触发 Record（若扫到 usage）。未 Attach 则无操作。
func (c *Collector) Close() {
	defer func() { recover() }()
	if c.scanner != nil {
		c.scanner.Close()
	}
}

// Stats 返回本请求累计的 token 用量及是否观测到 usage 事件。
// 在 Close 之后（即 Forward 返回后）调用，值为最终值。未 Attach 时返回 false。
func (c *Collector) Stats() (TokenUsage, bool) {
	if c.scanner == nil {
		return TokenUsage{}, false
	}
	return c.scanner.usage, c.scanner.sawStart
}

func today() string {
	return time.Now().Format("2006-01-02")
}

// LoadFile 读取 usage.json（只读，不持锁），供 ccp stats 查询。
func LoadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("usage 文件解析失败: %w", err)
	}
	return &f, nil
}
