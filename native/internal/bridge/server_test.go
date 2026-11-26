package bridge

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyRoutesSameIDsAndBroadcastsEvents(t *testing.T) {
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	dial := func(addr string) *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr, nil)
		if err != nil {
			t.Fatal(err)
		}
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		return ws
	}
	target := dial(b.TargetAddress())
	defer target.Close()
	a := dial(b.CDPAddress())
	defer a.Close()
	c := dial(b.CDPAddress())
	defer c.Close()
	for _, client := range []*websocket.Conn{a, c} {
		if err = client.WriteJSON(map[string]any{"id": 7, "method": "Runtime.evaluate", "params": map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	ids := []any{}
	for range 2 {
		_, raw, err := target.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		f, err := Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal([]byte(f.Payload), &m)
		ids = append(ids, m["id"])
	}
	if ids[0] == ids[1] {
		t.Fatal("wire ID collision")
	}
	for _, id := range ids {
		payload, _ := json.Marshal(map[string]any{"id": id, "result": map[string]any{"ok": true}})
		raw, _ := Encode(1, "chromeDevtoolsResult", string(payload))
		target.WriteMessage(websocket.BinaryMessage, raw)
	}
	for _, client := range []*websocket.Conn{a, c} {
		var m map[string]any
		if err = client.ReadJSON(&m); err != nil || m["id"] != float64(7) {
			t.Fatal(m, err)
		}
	}
	raw, _ := Encode(2, "chromeDevtoolsResult", `{"method":"Runtime.consoleAPICalled","params":{}}`)
	target.WriteMessage(websocket.BinaryMessage, raw)
	for _, client := range []*websocket.Conn{a, c} {
		var m map[string]any
		if err = client.ReadJSON(&m); err != nil || m["method"] != "Runtime.consoleAPICalled" {
			t.Fatal(m, err)
		}
	}
	a.WriteJSON(map[string]any{"id": 8, "method": "Page.getFrameTree"})
	var blocked map[string]any
	a.ReadJSON(&blocked)
	encoded, _ := json.Marshal(blocked)
	if !strings.Contains(string(encoded), "disabled") {
		t.Fatal(blocked)
	}
	if b.Pending() != 0 {
		t.Fatal("route leak")
	}
}

func TestNewestSetupContextOwnsCDPCommandsAndResponses(t *testing.T) {
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	dial := func(addr string) *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr, nil)
		if err != nil {
			t.Fatal(err)
		}
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		return ws
	}
	source := dial(b.TargetAddress())
	defer source.Close()
	target := dial(b.TargetAddress())
	defer target.Close()
	client := dial(b.CDPAddress())
	defer client.Close()

	setup := func(ws *websocket.Conn, seq uint64) {
		raw, err := Encode(seq, "setupContext", "{}")
		if err != nil {
			t.Fatal(err)
		}
		if err = ws.WriteMessage(websocket.BinaryMessage, raw); err != nil {
			t.Fatal(err)
		}
		for range 4 {
			_, raw, err = ws.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(raw); err != nil {
				t.Fatalf("invalid setup command: %v", err)
			}
		}
	}
	setup(source, 1)
	setup(target, 2)

	if err := client.WriteJSON(map[string]any{"id": 7, "method": "Runtime.evaluate", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	_, raw, err := target.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := Decode(raw)
	if err != nil || frame.Category != "chromeDevtools" {
		t.Fatalf("active target did not receive CDP command: %#v, %v", frame, err)
	}
	var command struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(frame.Payload), &command); err != nil || command.ID == 0 {
		t.Fatalf("invalid forwarded command %q: %v", frame.Payload, err)
	}
	source.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := source.ReadMessage(); err == nil {
		t.Fatal("stale source received a command for the active target")
	}
	source.SetReadDeadline(time.Now().Add(3 * time.Second))

	// A stale source reply with the active request's rewritten ID must not
	// consume the route or reach the CDP client.
	stale, _ := Encode(3, "chromeDevtoolsResult", `{"id":`+strconv.FormatUint(command.ID, 10)+`,"result":{"source":"old"}}`)
	if err := source.WriteMessage(websocket.BinaryMessage, stale); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := b.Pending(); got != 1 {
		t.Fatalf("stale source response consumed active route: pending=%d", got)
	}
	active, _ := Encode(4, "chromeDevtoolsResult", `{"id":`+strconv.FormatUint(command.ID, 10)+`,"result":{"source":"active"}}`)
	if err := target.WriteMessage(websocket.BinaryMessage, active); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := client.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	if response["id"] != float64(7) || result["source"] != "active" {
		t.Fatalf("unexpected active target reply: %#v", response)
	}
}

func TestStaleTargetEventsAreDropped(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", func(name string, _ map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		seen[name] = true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	dial := func(addr string) *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr, nil)
		if err != nil {
			t.Fatal(err)
		}
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		return ws
	}
	source := dial(b.TargetAddress())
	defer source.Close()
	active := dial(b.TargetAddress())
	defer active.Close()
	client := dial(b.CDPAddress())
	defer client.Close()
	setup := func(ws *websocket.Conn, seq uint64) {
		frame, err := Encode(seq, "setupContext", "{}")
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			t.Fatal(err)
		}
		for range 4 {
			if _, _, err := ws.ReadMessage(); err != nil {
				t.Fatal(err)
			}
		}
	}
	setup(source, 1)
	setup(active, 2)
	frame, _ := Encode(3, "chromeDevtoolsResult", `{"method":"Runtime.consoleAPICalled","params":{"source":"old"}}`)
	if err := source.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for b.Stats()["stale_event"] == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Stats()["stale_event"] != 1 {
		t.Fatal("stale event was not rejected")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		mu.Lock()
		disconnected := seen["miniapp.target_disconnected"]
		cleared := seen["miniapp.disconnected"]
		mu.Unlock()
		if disconnected {
			if cleared {
				t.Fatal("non-active source disconnect cleared the active target")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source disconnect was not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var got map[string]any
	if err := client.ReadJSON(&got); err == nil {
		t.Fatalf("stale event reached client: %#v", got)
	}
}

// The real Runtime console failure cannot be located without diagnostics at the
// bridge boundary: unknown categories, upstream events and unrouted replies
// must all be observable, not silently dropped.
func TestBridgeDiagnostics(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]map[string]any{}
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", func(name string, fields map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		seen[name] = fields
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	dial := func(addr string) *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr, nil)
		if err != nil {
			t.Fatal(err)
		}
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		return ws
	}
	target := dial(b.TargetAddress())
	defer target.Close()
	client := dial(b.CDPAddress())
	defer client.Close()
	for _, f := range []struct {
		seq      uint64
		category string
		payload  string
	}{
		{1, "mysteryCategory", "{}"},
		{2, "chromeDevtoolsResult", `{"method":"Runtime.consoleAPICalled","params":{},"sessionId":"ctx-7"}`},
		{3, "chromeDevtoolsResult", `{"id":777,"result":{}}`},
	} {
		raw, err := Encode(f.seq, f.category, f.payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := target.WriteMessage(websocket.BinaryMessage, raw); err != nil {
			t.Fatal(err)
		}
	}
	var got map[string]any
	if err := client.ReadJSON(&got); err != nil || got["method"] != "Runtime.consoleAPICalled" {
		t.Fatal(got, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		_, category := seen["miniapp.category"]
		_, event := seen["bridge.event"]
		_, unrouted := seen["bridge.unrouted"]
		mu.Unlock()
		if category && event && unrouted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing bridge diagnostics: %v", seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	category := seen["miniapp.category"]
	event := seen["bridge.event"]
	unrouted := seen["bridge.unrouted"]
	mu.Unlock()
	if category["category"] != "mysteryCategory" {
		t.Fatal("unknown category not reported", category)
	}
	if event["method"] != "Runtime.consoleAPICalled" || event["session_id"] != "ctx-7" {
		t.Fatal("upstream event not reported", event)
	}
	if unrouted["id"] != uint64(777) {
		t.Fatal("unrouted reply not reported", unrouted)
	}
	stats := b.Stats()
	if stats["category:mysteryCategory"] != 1 || stats["event:Runtime.consoleAPICalled"] != 1 || stats["unrouted"] != 1 {
		t.Fatal("diagnostic counters missing", stats)
	}
	b.Close()
	mu.Lock()
	_, ok := seen["bridge.stats"]
	mu.Unlock()
	if !ok {
		t.Fatal("final stats not emitted")
	}
}

// A private-protocol result may wrap the CDP message instead of being the CDP
// message itself. Record the shape (keys only) so the real Runtime replay can
// tell whether console events are hidden inside such frames, without logging
// any payload contents.
func TestBridgeUnknownUpstreamShape(t *testing.T) {
	var mu sync.Mutex
	shapes := []map[string]any{}
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", func(name string, fields map[string]any) {
		if name != "bridge.unknown_upstream" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		shapes = append(shapes, fields)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	target, _, err := websocket.DefaultDialer.Dial("ws://"+b.TargetAddress(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	frame, err := Encode(1, "chromeDevtoolsResult", `{"op_id":1,"payload":{"method":"Runtime.consoleAPICalled"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(shapes)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unknown upstream shape not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	shape := shapes[0]
	mu.Unlock()
	if shape["has_console"] != true {
		t.Fatal("console marker not detected in wrapped frame", shape)
	}
	if stats := b.Stats(); stats["upstream_without_method"] != 1 {
		t.Fatal("unshaped frame not counted", stats)
	}
}

// The real Runtime may use a different inner protobuf layout than the frozen
// Node fixtures. Report field metadata (never contents) so a replay can show
// which field actually carries the CDP message.
func TestBridgeFrameSummaryIsReported(t *testing.T) {
	var mu sync.Mutex
	var inner []string
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", func(name string, fields map[string]any) {
		if name != "bridge.unknown_upstream" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if row, ok := fields["inner_fields"].([]string); ok {
			inner = row
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	target, _, err := websocket.DefaultDialer.Dial("ws://"+b.TargetAddress(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	frame, err := Encode(1, "chromeDevtoolsResult", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(inner)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inner field summary not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inner) < 2 {
		t.Fatal("expected opId/payload/jscontextId metadata", inner)
	}
}

// Several DebugMessages may be concatenated inside one websocket frame. If so,
// Decode must not silently keep only the last one. Report outer field metadata
// (never contents) so a real replay can prove or disprove that layout.
func TestBridgeOuterFieldSummary(t *testing.T) {
	var mu sync.Mutex
	var outer []string
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", func(name string, fields map[string]any) {
		if name != "bridge.unknown_upstream" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if row, ok := fields["outer_fields"].([]string); ok {
			outer = row
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	target, _, err := websocket.DefaultDialer.Dial("ws://"+b.TargetAddress(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	frame, err := Encode(1, "chromeDevtoolsResult", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(outer)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("outer field summary not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(outer) < 3 {
		t.Fatal("expected seq/category/data/compression metadata", outer)
	}
}

// Losing the private-protocol target must be observable by the session state
// machine, otherwise debug_ready stays true after the upstream disconnects.
func TestBridgeReportsDisconnect(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", func(name string, _ map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		seen[name] = true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	target, _, err := websocket.DefaultDialer.Dial("ws://"+b.TargetAddress(), nil)
	if err != nil {
		t.Fatal(err)
	}
	target.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		ok := seen["miniapp.disconnected"]
		mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("target disconnect not reported", seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBridgeStatsCountsCommands(t *testing.T) {
	b, err := Start(context.Background(), "127.0.0.1:0", "127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	target, _, err := websocket.DefaultDialer.Dial("ws://"+b.TargetAddress(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws://"+b.CDPAddress(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.WriteJSON(map[string]any{"id": 1, "method": "Runtime.enable", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b.Stats()["command:Runtime.enable"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("downstream command not counted", b.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
