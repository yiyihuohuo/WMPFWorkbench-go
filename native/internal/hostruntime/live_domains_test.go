package hostruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/cdp"
	"wmpfworkbench/internal/config"
)

func liveReady(t *testing.T) (*Manager, string) {
	t.Helper()
	root := os.Getenv("WMPF_TEST_LIVE_ROOT")
	cfgPath := os.Getenv("WMPF_TEST_LIVE_CONFIG")
	id := os.Getenv("WMPF_TEST_LIVE_APPID")
	if root == "" || cfgPath == "" || id == "" {
		t.Skip("set WMPF_TEST_LIVE_ROOT/CONFIG/APPID for the live domain probe")
	}
	p, err := app.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.New(cfgPath)
	m := New(p, cfg, account.New(cfg))
	hidden := true
	value, err := m.Launch(context.Background(), id, "", 120, &hidden)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	sid := value.(map[string]any)["session_id"].(string)
	deadline := time.Now().Add(85 * time.Second)
	for {
		rows := m.Sessions()
		row := rows[len(rows)-1]
		if row["state"] == "error" {
			m.Close()
			t.Fatal(row)
		}
		if row["state"] == "debug_ready" {
			return m, sid
		}
		if time.Now().After(deadline) {
			m.Close()
			t.Fatalf("runtime not ready: %v", row)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestLiveDebugDomainProbe records what the current runtime actually supports.
// Unsupported domains are logged, not asserted away: this is capability
// evidence, not a fixture-based success.
func TestLiveDebugDomainProbe(t *testing.T) {
	m, sid := liveReady(t)
	defer m.Close()
	rows := m.Sessions()
	row := rows[len(rows)-1]
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	conn, err := cdp.Dial(ctx, row["cdp_websocket"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	probe := func(name, method string, params any) {
		callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer callCancel()
		raw, err := conn.Send(callCtx, method, params, "")
		if err != nil {
			t.Logf("DOMAIN %-14s ERROR %v", name, err)
			return
		}
		text := string(raw)
		if len(text) > 200 {
			text = text[:200]
		}
		t.Logf("DOMAIN %-14s OK %s", name, text)
	}
	probe("screenshot", "Page.captureScreenshot", map[string]any{"format": "png"})
	objectID := ""
	if raw, err := conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": "({a:1})", "returnByValue": false}, ""); err != nil {
		t.Logf("DOMAIN %-14s ERROR %v", "object-eval", err)
	} else {
		var v struct {
			Result struct {
				ObjectID string `json:"objectId"`
			} `json:"result"`
		}
		json.Unmarshal(raw, &v)
		objectID = v.Result.ObjectID
		t.Logf("DOMAIN %-14s OK objectId=%q", "object-eval", objectID)
	}
	if objectID != "" {
		probe("object", "Runtime.getProperties", map[string]any{"objectId": objectID, "ownProperties": true})
	}
	probe("click", "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": 40, "y": 40, "button": "left", "clickCount": 1})
	probe("breakpoint", "Debugger.setBreakpointByUrl", map[string]any{"lineNumber": 1, "url": "page-frame.html"})
	probe("network-enable", "Network.enable", nil)
	cursor := ""
	if page, err := conn.Buffer.Read(ctx, cdp.ReadOptions{Since: "now"}); err == nil {
		cursor = page.NextCursor
	}
	_, _ = conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": "fetch('https://example.invalid/probe').catch(function(){})", "returnByValue": true}, "")
	events, err := conn.Buffer.Read(ctx, cdp.ReadOptions{Methods: []string{"Network.*"}, Since: cursor, WaitMS: 2000, MaxBytes: 32000})
	if err != nil {
		t.Logf("DOMAIN %-14s ERROR %v", "network-events", err)
	} else {
		t.Logf("DOMAIN %-14s count=%d", "network-events", len(events.Events))
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	closed, err := m.CloseSession(closeCtx, sid)
	t.Logf("CLOSE %v %v", closed, err)
	if err != nil {
		t.Error(err)
	}
	if raw, err := os.ReadFile(filepath.Join(m.Paths.Data, "desktop", "sessions", sid, "observation", "bridge.jsonl")); err == nil {
		t.Logf("bridge diagnostics bytes=%d", len(raw))
	}
}
