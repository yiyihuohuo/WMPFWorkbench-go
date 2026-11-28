package export

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"wmpfworkbench/internal/dumpclient"
)

func testClient(base string) *dumpclient.Client {
	return dumpclient.New(dumpclient.Options{BaseURL: base, AutoStart: false})
}

func sidecarOK(w http.ResponseWriter, command string, data map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-api-protocol", "1")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "command": command, "version": "1.0.0", "protocol": 1, "data": data,
	})
}

func TestStartReturnsImmediatelyAndStatusTracks(t *testing.T) {
	// 模拟一个耗时 400ms 的下端：Start 必须立即返回，Status 能看到 running→done
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ilink/download" {
			time.Sleep(400 * time.Millisecond)
			sidecarOK(w, "ilink download", map[string]any{
				"appid": "wx0000000000000006", "outDir": "/tmp/out"})
			return
		}
		sidecarOK(w, "ilink status", map[string]any{})
	}))
	defer srv.Close()
	m := New(testClient(srv.URL))
	start := time.Now()
	resp, err := m.Start(context.Background(), Options{AppID: "wx0000000000000006", Out: "/tmp/out"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Start blocked")
	}
	id, _ := resp["task_id"].(string)
	if id == "" {
		t.Fatal("no task_id")
	}
	st := m.Status(id)
	if st["state"] != "queued" && st["state"] != "running" {
		t.Fatalf("initial state=%v", st["state"])
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st = m.Status(id)
		if st["state"] == "done" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if st["state"] != "done" {
		t.Fatalf("never done: %+v", st)
	}
	res, _ := st["result"].(map[string]any)
	if res["out_dir"] != "/tmp/out" {
		t.Fatalf("result=%+v", res)
	}
}

func TestStatusUnknownAndCancel(t *testing.T) {
	m := New(testClient("http://127.0.0.1:1"))
	st := m.Status("nonexistent")
	if st["state"] != "unknown" {
		t.Fatalf("state=%v", st["state"])
	}
	if r := m.Cancel("nonexistent"); r["ok"] != false {
		t.Fatalf("cancel=%v", r)
	}
}

func TestHistoryBounded(t *testing.T) {
	m := New(testClient("http://127.0.0.1:1"))
	ids := make([]string, 0, taskHistoryLimit+3)
	for i := 0; i < taskHistoryLimit+3; i++ {
		resp, err := m.Start(context.Background(), Options{AppID: "wx0000000000000006", Out: "/tmp/out"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, resp["task_id"].(string))
	}
	m.mu.Lock()
	nTasks, nOrder := len(m.tasks), len(m.order)
	m.mu.Unlock()
	if nTasks != taskHistoryLimit || nOrder != taskHistoryLimit {
		t.Fatalf("history not bounded: tasks=%d order=%d", nTasks, nOrder)
	}
	if st := m.Status(ids[0]); st["state"] != "unknown" {
		t.Fatalf("evicted task still visible: %v", st)
	}
	if st := m.Status(ids[len(ids)-1]); st["state"] == "unknown" {
		t.Fatalf("newest task lost: %v", st)
	}
}

// Cancelling a task must release its download slot so queued tasks can run.
// We assert this from the client side (a fresh task reaches the download
// handler) because the server-side r.Context() cancellation is transport-
// timing dependent and not part of this manager's contract.
func TestCancelReleasesDownloadSlot(t *testing.T) {
	entered := make(chan struct{}, downloadSlots+1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ilink/download" {
			entered <- struct{}{}
			<-release // hold the slot until the test releases all downloads
			sidecarOK(w, "ilink download", map[string]any{})
			return
		}
		sidecarOK(w, "ilink status", map[string]any{})
	}))
	defer srv.Close()
	defer close(release)
	m := New(testClient(srv.URL))

	// Fill every slot with a blocked download.
	ids := make([]string, 0, downloadSlots)
	for i := 0; i < downloadSlots; i++ {
		resp, err := m.Start(context.Background(), Options{AppID: "wx0000000000000006", Out: "/tmp/out"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, resp["task_id"].(string))
	}
	for i := 0; i < downloadSlots; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("download handlers did not start")
		}
	}
	// One more task must be queued (cannot reach the handler yet).
	queued, err := m.Start(context.Background(), Options{AppID: "wx0000000000000006", Out: "/tmp/out"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("queued task acquired a slot while all slots were busy")
	case <-time.After(150 * time.Millisecond):
	}

	// Cancel one blocked task; its slot must be released and the queued task
	// must reach the handler.
	if st := m.Cancel(ids[0]); st["state"] != "cancelled" {
		t.Fatalf("cancel state=%v", st["state"])
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("queued task never got a released slot")
	}
	_ = queued
}

func TestConcurrentDownloadsBounded(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ilink/download" {
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			time.Sleep(120 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{}})
	}))
	defer srv.Close()
	m := New(testClient(srv.URL))
	for i := 0; i < 12; i++ {
		if _, err := m.Start(context.Background(), Options{AppID: "wx0000000000000006", Out: "/tmp/out"}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	peak := maxInFlight
	mu.Unlock()
	if peak > downloadSlots {
		t.Fatalf("concurrent downloads=%d exceed gate=%d", peak, downloadSlots)
	}
}
