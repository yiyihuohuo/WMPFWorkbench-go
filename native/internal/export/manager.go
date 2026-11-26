// Package export runs wxapkg-dump package downloads as background tasks so
// callers (MCP and GUI) return immediately and poll for progress instead of
// blocking an agent for minutes.
package export

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"time"
	"wmpfworkbench/internal/dumpclient"
)

type Options = dumpclient.DownloadOptions

// taskHistoryLimit bounds the in-memory status table. Oldest snapshots are
// evicted first; each snapshot is small (metadata only), so 20 entries keep
// the manager's own footprint at a few MB regardless of submission rate.
const taskHistoryLimit = 20

// downloadSlots bounds concurrent downloads. Without it a caller looping on
// export_mini_program would spawn unbounded goroutines, each opening a 32MB
// response body and unpacking a package to disk, overwhelming the sidecar.
const downloadSlots = 3

type task struct {
	id        string
	created   time.Time
	mu        sync.Mutex
	state     string // queued | running | done | failed | cancelled
	phase     string
	startedAt time.Time
	finished  time.Time
	result    dumpclient.DownloadResult
	reason    string
	cancel    context.CancelFunc
	progress  dumpclient.Progress // real counters from the sidecar SSE stream
}

func (t *task) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	elapsed := int64(0)
	if !t.startedAt.IsZero() {
		end := t.finished
		if end.IsZero() {
			end = time.Now()
		}
		elapsed = end.Sub(t.startedAt).Milliseconds()
	}
	out := map[string]any{
		"task_id": t.id, "state": t.state, "phase": t.phase,
		"elapsed_ms": elapsed, "created_at": t.created.Unix(),
	}
	// Real progress from the sidecar event stream (package/reconstruction
	// counters). Always present so the GUI can render a factual progress bar.
	if t.progress.Total > 0 {
		out["progress"] = map[string]any{
			"done": t.progress.Done, "total": t.progress.Total,
			"module": t.progress.Module, "stage": t.progress.Stage,
			"message": t.progress.Message,
		}
	} else if t.progress.Message != "" {
		out["progress"] = map[string]any{"stage": t.progress.Stage, "message": t.progress.Message}
	}
	if !t.finished.IsZero() {
		out["finished_at"] = t.finished.Unix()
	}
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
	sem    chan struct{} // concurrency gate for the blocking sidecar download
}

func New(c *dumpclient.Client) *Manager {
	return &Manager{
		client: c,
		tasks:  map[string]*task{},
		sem:    make(chan struct{}, downloadSlots),
	}
}

// Start enqueues a download and returns a task id immediately.
func (m *Manager) Start(ctx context.Context, o Options) (map[string]any, error) {
	if o.AppID == "" && o.Username == "" {
		return nil, fmt.Errorf("appid 或 username 必填")
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(b[:])
	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t := &task{id: id, state: "queued", phase: "等待处理", created: time.Now(), cancel: cancel}

	m.mu.Lock()
	m.tasks[id] = t
	m.order = append(m.order, id)
	if len(m.order) > taskHistoryLimit {
		old := m.order[0]
		m.order = m.order[1:]
		if oldt := m.tasks[old]; oldt != nil {
			// Eviction must also stop the background work: otherwise the
			// goroutine keeps downloading and writing to disk while no longer
			// reachable via Status/Cancel (a "ghost task").
			oldt.mu.Lock()
			if oldt.state == "queued" || oldt.state == "running" {
				oldt.state = "cancelled"
				oldt.phase = "已取消"
				oldt.finished = time.Now()
			}
			if oldt.cancel != nil {
				oldt.cancel()
			}
			oldt.mu.Unlock()
		}
		delete(m.tasks, old)
	}
	m.mu.Unlock()

	go func() {
		defer cancel()
		// Wait for a download slot. Queued goroutines hold only a small task
		// struct and a context timer; they exit early if evicted/cancelled.
		select {
		case m.sem <- struct{}{}:
			defer func() { <-m.sem }()
		case <-runCtx.Done():
			t.mu.Lock()
			t.state = "cancelled"
			t.phase = "已取消"
			t.finished = time.Now()
			t.mu.Unlock()
			return
		}

		t.mu.Lock()
		if t.state == "cancelled" {
			t.mu.Unlock()
			return
		}
		t.state = "running"
		t.phase = "连接导出服务"
		t.startedAt = time.Now()
		t.mu.Unlock()

		if err := m.client.Ensure(runCtx); err != nil {
			t.mu.Lock()
			t.state = "failed"
			t.phase = "失败"
			t.reason = err.Error()
			t.finished = time.Now()
			t.mu.Unlock()
			return
		}
		// Subscribe before starting the download so no progress events are lost.
		// The stream is lossy by design; only the latest structured counter is
		// kept in the task snapshot.
		evCtx, stopEvents := context.WithCancel(runCtx)
		defer stopEvents()
		go func() {
			_ = m.client.SubscribeEvents(evCtx, func(p dumpclient.Progress) {
				t.mu.Lock()
				t.progress = p
				switch p.Stage {
				case "download":
					t.phase = "下载分包 " + strconv.Itoa(p.Done) + "/" + strconv.Itoa(p.Total)
				case "reconstruct":
					t.phase = "解包重建 " + strconv.Itoa(p.Done) + "/" + strconv.Itoa(p.Total)
				case "done":
					t.phase = "下载完成，等待解包"
				}
				t.mu.Unlock()
			})
		}()
		t.mu.Lock()
		t.phase = "连接导出服务，等待分包列表"
		t.mu.Unlock()
		res, err := m.client.Download(runCtx, o)
		stopEvents()
		t.mu.Lock()
		t.finished = time.Now()
		if err != nil {
			if runCtx.Err() != nil {
				t.state = "cancelled"
				t.phase = "已取消"
				t.reason = "任务已取消"
			} else {
				t.state = "failed"
				t.phase = "失败"
				t.reason = err.Error()
			}
		} else {
			t.state = "done"
			t.phase = "完成"
			t.result = res
		}
		t.mu.Unlock()
	}()
	return map[string]any{"ok": true, "task_id": id, "state": "queued",
		"hint": "用 export_status 查询进度"}, nil
}

// Status returns one task's snapshot, or the most recent task when id is empty.
func (m *Manager) Status(id string) map[string]any {
	m.mu.Lock()
	if id == "" && len(m.order) > 0 {
		id = m.order[len(m.order)-1]
	}
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
	m.mu.Lock()
	t := m.tasks[id]
	m.mu.Unlock()
	if t == nil {
		return map[string]any{"ok": false, "reason": "任务不存在"}
	}
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	if t.state == "queued" || t.state == "running" {
		t.state = "cancelled"
		t.phase = "已取消"
		t.finished = time.Now()
	}
	t.mu.Unlock()
	return t.snapshot()
}

// List returns all task snapshots newest-first (submission order), bounded by
// taskHistoryLimit. Snapshots are taken under m.mu so a concurrent eviction
// cannot invalidate an id between selection and lookup.
func (m *Manager) List(limit int) []any {
	m.mu.Lock()
	out := make([]any, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		if t := m.tasks[m.order[i]]; t != nil {
			out = append(out, t.snapshot())
		}
	}
	m.mu.Unlock()
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
