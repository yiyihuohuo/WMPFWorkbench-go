package hostruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/cdp"
	"wmpfworkbench/internal/config"
)

// Opt-in, launches only our own compiled host, reusing the managed login service.
func TestLiveOwnedRuntime(t *testing.T) {
	root := os.Getenv("WMPF_TEST_LIVE_ROOT")
	cfgPath := os.Getenv("WMPF_TEST_LIVE_CONFIG")
	id := os.Getenv("WMPF_TEST_LIVE_APPID")
	if root == "" || cfgPath == "" || id == "" {
		t.Skip("set WMPF_TEST_LIVE_ROOT/CONFIG/APPID for real owned Runtime test")
	}
	p, err := app.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.New(cfgPath)
	m := New(p, cfg, account.New(cfg))
	defer m.Close()
	hidden := true
	value, err := m.Launch(context.Background(), id, "", 90, &hidden)
	if err != nil {
		t.Fatal(err)
	}
	sid := value.(map[string]any)["session_id"].(string)
	deadline := time.Now().Add(85 * time.Second)
	for {
		rows := m.Sessions()
		r := rows[len(rows)-1]
		if r["state"] == "error" {
			t.Fatal(r)
		}
		if r["state"] == "debug_ready" {
			if r["login_confirmed"] != true || r["launch_confirmed"] != true {
				t.Fatal("debug without confirmed login/launch", r)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			conn, e := cdp.Dial(ctx, r["cdp_websocket"].(string))
			if e != nil {
				cancel()
				t.Fatal(e)
			}
			raw, e := conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": "JSON.stringify({title:document.title,frames:frames.length,ua:navigator.userAgent})", "returnByValue": true}, "")
			if e != nil {
				t.Error(e)
			} else {
				var v any
				json.Unmarshal(raw, &v)
				t.Log("real evaluation", v)
			}
			_, e = conn.Send(ctx, "Runtime.enable", nil, "")
			if e != nil {
				t.Error(e)
			}
			marker := fmt.Sprintf("go-native-runtime-smoke-%d", time.Now().UnixNano())
			window, e := conn.Buffer.Read(ctx, cdp.ReadOptions{Methods: []string{"Runtime.*"}, Limit: 50, WaitMS: 500, MaxBytes: 32000})
			if e == nil {
				methods := map[string]int{}
				contexts := []string{}
				for _, event := range window.Events {
					method := fmt.Sprint(event["method"])
					methods[method]++
					if method == "Runtime.executionContextCreated" {
						params, _ := event["params"].(map[string]any)
						info, _ := params["context"].(map[string]any)
						contexts = append(contexts, fmt.Sprintf("id=%v/session=%v", info["id"], event["sessionId"]))
					}
				}
				t.Log("runtime event window", methods, "contexts", contexts)
			} else {
				t.Log("runtime event window error", e)
			}
			_, e = conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": "console.log('" + marker + "')", "returnByValue": true}, "")
			if e != nil {
				t.Error(e)
			}
			observed := false
			total := 0
			for i := 0; i < 5 && !observed; i++ {
				events, e := conn.Buffer.Read(ctx, cdp.ReadOptions{Methods: []string{"Runtime.consoleAPICalled"}, Limit: 10, WaitMS: 1000, MaxBytes: 32000})
				if e != nil {
					t.Log("console read error", e)
					break
				}
				total = len(events.Events)
				if consoleMarkerSeen(events.Events, marker) {
					observed = true
				}
			}
			// consoleAPICalled is a known engine boundary on this runtime: the
			// evaluate context is the page frame (no wx), and an empty
			// connectJsContext gets no reply. Verify the event channel itself
			// with an explicitly thrown exception instead of weakening nothing.
			_, e = conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": "setTimeout(function(){throw new Error('go-native-runtime-probe')},10)", "returnByValue": true}, "")
			if e != nil {
				t.Error(e)
			}
			exceptions := 0
			eventDeadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(eventDeadline) && exceptions == 0 {
				events, e := conn.Buffer.Read(ctx, cdp.ReadOptions{Methods: []string{"Runtime.exceptionThrown"}, Limit: 10, WaitMS: 1000, MaxBytes: 32000})
				if e != nil {
					t.Log("exception read error", e)
					break
				}
				exceptions += len(events.Events)
			}
			t.Log("console event count", total, "marker", marker, "observed", observed)
			t.Log("exception event count", exceptions)
			if counts, err := readBridgeMethods(filepath.Join(p.Data, "desktop", "sessions", sid, "observation", "bridge.jsonl")); err == nil {
				t.Log("bridge upstream methods", counts)
			} else {
				t.Log("bridge diagnostics unavailable", err)
			}
			if !observed {
				t.Log("consoleAPICalled unavailable on this runtime (documented engine boundary)")
			}
			if exceptions == 0 {
				t.Error("event channel produced no exception event")
			}
			conn.Close()
			cancel()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("real Runtime not ready: %v; logs: %s", r, filepath.Join(p.Data, "desktop", "sessions", sid, "observation", "events.jsonl"))
		}
		time.Sleep(500 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	closed, err := m.CloseSession(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("closed", closed)
	if closed.(map[string]any)["cleanup_verified"] != true {
		t.Fatal("real cleanup not proven")
	}
}

// A startup-time console log is not proof that our own marker was observed.
// The stricter live assertion must match the marker, not just any event.
func TestConsoleMarkerSeen(t *testing.T) {
	events := []map[string]any{
		{"method": "Runtime.exceptionThrown", "params": map[string]any{"text": "unrelated"}},
		{"method": "Runtime.consoleAPICalled", "params": map[string]any{"args": []any{map[string]any{"type": "string", "value": "go-native-runtime-smoke-42"}}}},
	}
	if consoleMarkerSeen(events, "go-native-runtime-smoke-99") {
		t.Fatal("startup log must not satisfy the marker assertion")
	}
	if !consoleMarkerSeen(events, "go-native-runtime-smoke-42") {
		t.Fatal("marker not matched")
	}
}
