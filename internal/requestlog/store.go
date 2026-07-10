package requestlog

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// QueryParams 查询过滤条件。Limit 默认 100，倒序（最新在前）。
type QueryParams struct {
	Project string
	Since   time.Time // 含；零值=不限
	Until   time.Time // 含；零值=不限
	Limit   int
	Offset  int
}

// Row 查询结果（带 ID 与 TS）。
type Row struct {
	ID           int64
	TS           time.Time
	Project      string
	Method       string
	Path         string
	Model        string
	Upstream     string
	RealModel    string
	Status       int
	DurationMs   int64
	Error        string
	RequestBody  string
	ResponseBody string
}

const schema = `
CREATE TABLE IF NOT EXISTS requests (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  ts            INTEGER NOT NULL,
  project       TEXT    NOT NULL,
  method        TEXT,
  path          TEXT,
  model         TEXT,
  upstream      TEXT,
  real_model    TEXT,
  status        INTEGER,
  duration_ms   INTEGER,
  error         TEXT,
  request_body  TEXT,
  response_body TEXT
);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS idx_requests_project ON requests(project, ts);
`

const channelBuf = 1024

// syncReq 用于 Flush 同步：writeLoop 处理完它之前所有 Entry 后关闭 done。
type syncReq struct{ done chan struct{} }

// sub 一个订阅者。
type sub struct {
	ch      chan Row
	project string // 空=全部
}

// Store 请求日志存储：异步写入 SQLite（WAL），支持查询与订阅。
type Store struct {
	db       *sql.DB
	ch       chan any // Entry 或 syncReq
	stopCh   chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	maxDays  int
	subsMu   sync.Mutex
	nextSub  int
	subs     map[int]sub
}

// NewStore 打开/创建 SQLite 库，建表，启动后台写入 goroutine。
func NewStore(path string, maxDays int) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("创建 requestlog 目录失败: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 requestlog 库失败: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("建表失败: %w", err)
	}
	if maxDays < 0 {
		maxDays = 0
	}
	s := &Store{
		db:      db,
		ch:      make(chan any, channelBuf),
		stopCh:  make(chan struct{}),
		done:    make(chan struct{}),
		maxDays: maxDays,
		subs:    make(map[int]sub),
	}
	// 确保 db 文件权限为 0600；失败不阻断初始化。
	if err := os.Chmod(path, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog chmod 0600 失败: %v\n", err)
	}
	go s.writeLoop()
	return s, nil
}

// Record 异步投递一条日志（非阻塞：channel 满则丢弃并记 stderr）。
func (s *Store) Record(e Entry) {
	select {
	case s.ch <- e:
	default:
		fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog 缓冲已满，丢弃一条日志\n")
	}
}

// Flush 阻塞直到当前已投递的日志全部落库（测试用）。
// 若 writeLoop 已退出（Close 后），直接返回避免死锁。
func (s *Store) Flush() {
	done := make(chan struct{})
	select {
	case s.ch <- syncReq{done: done}:
		select {
		case <-done:
		case <-s.done:
		}
	case <-s.done:
	}
}

func (s *Store) writeLoop() {
	batch := make([]Entry, 0, 64)
	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		s.insertBatch(batch)
		batch = batch[:0]
	}
	for {
		select {
		case v := <-s.ch:
			switch x := v.(type) {
			case Entry:
				batch = append(batch, x)
				if len(batch) >= 64 {
					flushBatch()
				}
			case syncReq:
				flushBatch()
				close(x.done)
			}
		case <-time.After(500 * time.Millisecond):
			flushBatch()
		case <-s.stopCh:
			// 排空 channel 中残留的 Entry/syncReq，避免数据丢失。
			for {
				select {
				case v := <-s.ch:
					switch x := v.(type) {
					case Entry:
						batch = append(batch, x)
						if len(batch) >= 64 {
							flushBatch()
						}
					case syncReq:
						flushBatch()
						close(x.done)
					}
				default:
					flushBatch()
					close(s.done)
					return
				}
			}
		}
	}
}

func (s *Store) insertBatch(batch []Entry) {
	tx, err := s.db.Begin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog 事务失败: %v\n", err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO requests(ts,project,method,path,model,upstream,real_model,status,duration_ms,error,request_body,response_body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog prepare 失败: %v\n", err)
		return
	}
	defer stmt.Close()
	now := time.Now()
	for _, e := range batch {
		if _, err := stmt.Exec(now.UnixMilli(), e.Project, e.Method, e.Path, e.Model, e.Upstream, e.RealModel, e.Status, e.DurationMs, e.Error, e.RequestBody, e.ResponseBody); err != nil {
			fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog 插入失败: %v\n", err)
			continue
		}
		s.broadcast(Row{TS: now, Project: e.Project, Method: e.Method, Path: e.Path, Model: e.Model, Upstream: e.Upstream, RealModel: e.RealModel, Status: e.Status, DurationMs: e.DurationMs, Error: e.Error, RequestBody: e.RequestBody, ResponseBody: e.ResponseBody})
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog 提交失败: %v\n", err)
		return
	}
	s.maybeClean(now)
}

// Subscribe 订阅新写入的行（按 project 过滤，空=全部）。返回 channel 与取消函数。
// channel 缓冲 64；消费者慢时非阻塞丢弃（实时日志容忍丢失）。
func (s *Store) Subscribe(project string) (<-chan Row, func()) {
	ch := make(chan Row, 64)
	s.subsMu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = sub{ch: ch, project: project}
	s.subsMu.Unlock()
	cancel := func() {
		s.subsMu.Lock()
		if _, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(ch)
		}
		s.subsMu.Unlock()
	}
	return ch, cancel
}

// broadcast 推送给订阅者。
func (s *Store) broadcast(r Row) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for _, su := range s.subs {
		if su.project != "" && r.Project != su.project {
			continue
		}
		select {
		case su.ch <- r:
		default:
		}
	}
}

// maybeClean 惰性删除超过 maxDays 的行（maxDays<=0 永久保留）。
func (s *Store) maybeClean(now time.Time) {
	if s.maxDays <= 0 {
		return
	}
	cutoff := now.AddDate(0, 0, -s.maxDays).UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM requests WHERE ts < ?`, cutoff); err != nil {
		fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog 清理失败: %v\n", err)
	}
}

// Query 按条件分页查询，倒序（最新在前）。
func (s *Store) Query(p QueryParams) []Row {
	if p.Limit <= 0 {
		p.Limit = 100
	}
	q := `SELECT id,ts,project,method,path,model,upstream,real_model,status,duration_ms,error,request_body,response_body FROM requests WHERE 1=1`
	args := []any{}
	if p.Project != "" {
		q += " AND project=?"
		args = append(args, p.Project)
	}
	if !p.Since.IsZero() {
		q += " AND ts>=?"
		args = append(args, p.Since.UnixMilli())
	}
	if !p.Until.IsZero() {
		q += " AND ts<=?"
		args = append(args, p.Until.UnixMilli())
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, p.Limit, p.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ccp-proxy] requestlog 查询失败: %v\n", err)
		return nil
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var ts int64
		if err := rows.Scan(&r.ID, &ts, &r.Project, &r.Method, &r.Path, &r.Model, &r.Upstream, &r.RealModel, &r.Status, &r.DurationMs, &r.Error, &r.RequestBody, &r.ResponseBody); err != nil {
			continue
		}
		r.TS = time.UnixMilli(ts)
		out = append(out, r)
	}
	return out
}

// Close 停止写入 goroutine 并关闭 db。幂等。
func (s *Store) Close() error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	<-s.done
	return s.db.Close()
}
