package export

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"wmpfworkbench/internal/dumpclient"
)

func testClient(base string) *dumpclient.Client {
	return dumpclient.New(dumpclient.Options{BaseURL: base, AutoStart: false})
}

func TestStartReturnsImmediatelyAndStatusTracks(t *testing.T) {
	// 模拟一个耗时 400ms 的下端：Start 必须立即返回，Status 能看到 running→done
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ilink/download" {
			time.Sleep(400 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{
				"appid": "wx0000000000000006", "outDir": "/tmp/out"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{}})
	}))
	defer srv.Close()
	m := New(testClient(srv.URL))
	start := time.Now()
	resp, err := m.Start(context.Background(), Options{AppID: "wx0000000000000006", Out: "/tmp/out"})
	if err != nil { t.Fatal(err) }
	if time.Since(start) > 100*time.Millisecond { t.Fatal("Start blocked") }
	id, _ := resp["task_id"].(string)
	if id == "" { t.Fatal("no task_id") }
	st := m.Status(id)
	if st["state"] != "queued" && st["state"] != "running" { t.Fatalf("initial state=%v", st["state"]) }
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st = m.Status(id)
		if st["state"] == "done" { break }
		time.Sleep(30 * time.Millisecond)
	}
	if st["state"] != "done" { t.Fatalf("never done: %+v", st) }
	res, _ := st["result"].(map[string]any)
	if res["out_dir"] != "/tmp/out" { t.Fatalf("result=%+v", res) }
}

func TestStatusUnknownAndCancel(t *testing.T) {
	m := New(testClient("http://127.0.0.1:1"))
	st := m.Status("nonexistent")
	if st["state"] != "unknown" { t.Fatalf("state=%v", st["state"]) }
	if r := m.Cancel("nonexistent"); r["ok"] != false { t.Fatalf("cancel=%v", r) }
}
