package hostruntime

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wmpfworkbench/internal/cdp"
)

// TestLiveInteractionProbe verifies click delivery, debugger pause and XWeb
// netlog start/stop against a real session. Every step is logged with its raw
// response; unsupported commands are recorded, not asserted away.
func TestLiveInteractionProbe(t *testing.T) {
	m, sid := liveReady(t)
	defer m.Close()
	rows := m.Sessions()
	row := rows[len(rows)-1]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := cdp.Dial(ctx, row["cdp_websocket"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	eval := func(expression string) string {
		raw, err := conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, "")
		if err != nil {
			return "ERROR: " + err.Error()
		}
		text := string(raw)
		if len(text) > 200 {
			text = text[:200]
		}
		return text
	}
	probe := func(name, method string, params any) {
		callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer callCancel()
		raw, err := conn.Send(callCtx, method, params, "")
		if err != nil {
			t.Logf("PROBE %-22s ERROR %v", name, err)
			return
		}
		text := string(raw)
		if len(text) > 240 {
			text = text[:240]
		}
		t.Logf("PROBE %-22s %s", name, text)
	}

	// Click: install a capture listener, dispatch a real input event, count hits.
	t.Log("CLICK install:", eval("window.__hits=0;document.addEventListener('click',function(){window.__hits++},true);'ok'"))
	probe("click-pressed", "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": 120, "y": 120, "button": "left", "clickCount": 1})
	probe("click-released", "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": 120, "y": 120, "button": "left", "clickCount": 1})
	t.Log("CLICK hits:", eval("window.__hits"))

	// Breakpoint/pause: force a pause and require an actual Debugger.paused event.
	probe("debugger-enable", "Debugger.enable", nil)
	cursor := ""
	if page, err := conn.Buffer.Read(ctx, cdp.ReadOptions{Since: "now"}); err == nil {
		cursor = page.NextCursor
	}
	probe("debugger-pause", "Debugger.pause", nil)
	if paused, err := conn.Buffer.Read(ctx, cdp.ReadOptions{Methods: []string{"Debugger.paused"}, Since: cursor, WaitMS: 2000, MaxBytes: 32000}); err != nil {
		t.Logf("PAUSED read error %v", err)
	} else {
		t.Logf("PAUSED events=%d", len(paused.Events))
	}
	probe("debugger-resume", "Debugger.resume", nil)

	// NetLog: use the runtime's own XWeb commands and check the filesystem.
	netlogDir := filepath.Join(os.Getenv("WMPF_RUNTIME_DIR"), "log", "net-export")
	probe("netlog-start", "XWeb.NetLogStart", map[string]any{"path": netlogDir, "captureMode": "Default", "categories": []string{"*"}, "recordExperiments": false})
	probe("netlog-status", "XWeb.NetLogStatus", map[string]any{})
	probe("netlog-path", "XWeb.NetLogPath", map[string]any{})
	eval("fetch('https://example.invalid/netlog-probe').catch(function(){})")
	time.Sleep(1500 * time.Millisecond)
	probe("netlog-stop", "XWeb.NetLogStop", map[string]any{})
	files, total := 0, int64(0)
	filepath.WalkDir(netlogDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	t.Logf("NETLOG files=%d bytes=%d dir=%s", files, total, netlogDir)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	closed, err := m.CloseSession(closeCtx, sid)
	t.Logf("CLOSE %v %v", closed, err)
}
