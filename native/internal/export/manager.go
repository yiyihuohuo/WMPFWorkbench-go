// Package export runs wxapkg-dump package downloads as background tasks so
// callers (MCP and GUI) return immediately and poll for progress instead of
// blocking an agent for minutes.
package export

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
	"wmpfworkbench/internal/dumpclient"
)

type Options = dumpclient.DownloadOptions

type task struct {
	id        string
	mu        sync.Mutex
	state     string // queued | running | done | failed | cancelled
	phase     string
	startedAt time.Time
	finished  time.Time
	result    dumpclient.DownloadResult
	reason    string
	cancel    context.CancelFunc
}

func (t *task) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	elapsed := int64(0)
	if !t.startedAt.IsZero() {
		end := t.finished
		if end.IsZero() { end = time.Now() }
		elapsed = end.Sub(t.startedAt).Milliseconds()
	}
	out := map[string]any{
		"task_id": t.id, "state": t.state, "phase": t.phase,
		"elapsed_ms": elapsed, "created_at": t.startedAt.Unix(),
	}
	if !t.finished.IsZero() { out["finished_at"] = t.finished.Unix() }
	if t.state == "done" {
		out["ok"] = true
		out["result"] = map[string]any{"appid": t.result.AppID, "username": t.result.Username,
			"out_dir": t.result.OutDir, "unpacked_dir": t.result.UnpackedDir, "packages": t.result.Packages}
	} else if t.state == "failed" {
		out["ok"] = false
		out["reason"] = t.reason
	}
	return out
}

type Manager struct {
	client *dumpclient.Client
	mu     sync.Mutex
	tasks  map[string]*task
	order  []string
}

func New(c *dumpclient.Client) *Manager {
	return &Manager{client: c, tasks: map[string]*task{}}
}

// Start enqueues a download and returns a task id immediately.
func (m *Manager) Start(ctx context.Context, o Options) (map[string]any, error) {
	if o.AppID == "" && o.Username == "" { return nil, fmt.Errorf("appid 或 username 必填") }
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil { return nil, err }
	id := hex.EncodeToString(b[:])
	t := &task{id: id, state: "queued", phase: "等待处理"}
	m.mu.Lock()
	m.tasks[id] = t
	m.order = append(m.order, id)
	if len(m.order) > 20 {
		old := m.order[0]; m.order = m.order[1:]
		delete(m.tasks, old)
	}
	m.mu.Unlock()

	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		t.mu.Lock(); t.cancel = cancel; t.mu.Unlock()
		defer cancel()
		t.mu.Lock(); t.state = "running"; t.phase = "连接导出服务"; t.startedAt = time.Now(); t.mu.Unlock()

		if err := m.client.Ensure(runCtx); err != nil {
			t.mu.Lock(); t.state = "failed"; t.phase = "失败"; t.reason = err.Error(); t.finished = time.Now(); t.mu.Unlock()
			return
		}
		t.mu.Lock(); t.phase = "服务端下载并解包中（可能需要数十秒到数分钟）"; t.mu.Unlock()
		res, err := m.client.Download(runCtx, o)
		t.mu.Lock()
		t.finished = time.Now()
		if err != nil {
			t.state = "failed"; t.phase = "失败"; t.reason = err.Error()
		} else {
			t.state = "done"; t.phase = "完成"; t.result = res
		}
		t.mu.Unlock()
	}()
	return map[string]any{"ok": true, "task_id": id, "state": "queued",
		"hint": "用 export_status 查询进度"}, nil
}

// Status returns one task's snapshot, or the most recent task when id is empty.
func (m *Manager) Status(id string) map[string]any {
	m.mu.Lock()
	if id == "" && len(m.order) > 0 { id = m.order[len(m.order)-1] }
	t := m.tasks[id]
	m.mu.Unlock()
	if t == nil {
		return map[string]any{"ok": false, "state": "unknown", "task_id": id, "reason": "任务不存在"}
	}
	s := t.snapshot()
	s["ok"] = true
	return s
}

func (m *Manager) Cancel(id string) map[string]any {
	m.mu.Lock(); t := m.tasks[id]; m.mu.Unlock()
	if t == nil { return map[string]any{"ok": false, "reason": "任务不存在"} }
	t.mu.Lock()
	if t.cancel != nil { t.cancel() }
	if t.state == "queued" || t.state == "running" {
		t.state = "cancelled"; t.phase = "已取消"; t.finished = time.Now()
	}
	t.mu.Unlock()
	return t.snapshot()
}

// List returns all task snapshots newest-first.
func (m *Manager) List(limit int) []any {
	m.mu.Lock()
	ids := append([]string{}, m.order...)
	m.mu.Unlock()
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if limit > 0 && len(ids) > limit { ids = ids[:limit] }
	out := []any{}
	for _, id := range ids {
		if t := m.tasks[id]; t != nil { out = append(out, t.snapshot()) }
	}
	return out
}
