package requestlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T, maxDays int) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "r.db"), maxDays)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordAndQuery(t *testing.T) {
	s := newTestStore(t, 30)
	s.Record(Entry{
		Project: "p1", Method: "POST", Path: "/v1/messages",
		Model: "alias", Upstream: "up1", RealModel: "real",
		Status: 200, DurationMs: 12,
	})
	s.Flush()
	rows := s.Query(QueryParams{Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("期望 1 行，得到 %d", len(rows))
	}
	r := rows[0]
	if r.Project != "p1" || r.Status != 200 || r.Upstream != "up1" {
		t.Errorf("字段不匹配: %+v", r)
	}
	if r.ID == 0 || r.TS.IsZero() {
		t.Errorf("ID/TS 未填充: %+v", r)
	}
}

func TestRecordNonBlockingWhenChannelFull(t *testing.T) {
	s := newTestStore(t, 30)
	for i := 0; i < 2000; i++ {
		s.Record(Entry{Project: "p1", Status: 200})
	}
	s.Flush()
	rows := s.Query(QueryParams{Limit: 5000})
	if len(rows) == 0 {
		t.Errorf("应至少落库部分记录")
	}
}

func TestRecordConcurrent(t *testing.T) {
	s := newTestStore(t, 30)
	done := make(chan struct{})
	for g := 0; g < 10; g++ {
		go func() {
			for i := 0; i < 100; i++ {
				s.Record(Entry{Project: "p1", Status: 200})
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 10; g++ {
		<-done
	}
	s.Flush()
	rows := s.Query(QueryParams{Limit: 5000})
	if len(rows) != 1000 {
		t.Errorf("期望 1000 行，得到 %d", len(rows))
	}
}

func TestFlushAfterCloseDoesNotDeadlock(t *testing.T) {
	s := newTestStore(t, 30)
	s.Close()
	done := make(chan struct{})
	go func() {
		s.Flush() // must not block
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Flush after Close deadlocked")
	}
}

func TestCloseDrainsPendingBatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.db")
	s, err := NewStore(path, 30)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 0; i < 10; i++ {
		s.Record(Entry{Project: "p1", Status: 200})
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := NewStore(path, 30)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	rows := s2.Query(QueryParams{Limit: 100})
	if len(rows) != 10 {
		t.Errorf("期望 10 行（Close 前应 drain 挂起 batch），得到 %d", len(rows))
	}
}

func TestStoreDbFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.db")
	s, err := NewStore(path, 30)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("期望 0600，得到 %v", info.Mode().Perm())
	}
}
