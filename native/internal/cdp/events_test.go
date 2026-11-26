package cdp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func put(b *Buffer, method string, n int) {
	b.Append(map[string]any{"method": method, "params": map[string]any{"n": n}})
}
func TestEventsCursorPagingAndIndependentReaders(t *testing.T) {
	b := NewBuffer(4096, 8<<20)
	ctx := context.Background()
	start, _ := b.Read(ctx, ReadOptions{Since: "now"})
	put(b, "Runtime.consoleAPICalled", 1)
	put(b, "Network.requestWillBeSent", 2)
	put(b, "Page.loadEventFired", 3)
	put(b, "Network.loadingFailed", 4)
	a, err := b.Read(ctx, ReadOptions{Since: start.NextCursor, Methods: []string{"Network.*"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Events) != 1 || !a.HasMore || a.Events[0]["seq"] != float64(2) {
		t.Fatalf("bad page %+v", a)
	}
	next, _ := b.Read(ctx, ReadOptions{Since: a.NextCursor, Methods: []string{"Network.*"}})
	if next.HasMore || len(next.Events) != 1 || next.Events[0]["seq"] != float64(4) {
		t.Fatal(next)
	}
	all, _ := b.Read(ctx, ReadOptions{Since: start.NextCursor})
	if len(all.Events) != 4 {
		t.Fatal("consumed")
	}
	if _, err = NewBuffer(1, 2048).Read(ctx, ReadOptions{Since: a.NextCursor}); err != ErrCursor {
		t.Fatal(err)
	}
}
func TestEventsGapAndScriptIndex(t *testing.T) {
	b := NewBuffer(1, 2048)
	ctx := context.Background()
	start, _ := b.Read(ctx, ReadOptions{Since: "now"})
	b.Append(map[string]any{"method": "Debugger.scriptParsed", "params": map[string]any{"scriptId": "1", "url": "one.js"}})
	put(b, "Network.loadingFailed", 2)
	put(b, "Network.loadingFailed", 3)
	page, _ := b.Read(ctx, ReadOptions{Since: start.NextCursor})
	if !page.Gap || page.MissedEvents != 2 || len(b.Scripts()) != 1 {
		t.Fatal(page)
	}
}
func TestEventsBudgetNeverSkipsAndTruncates(t *testing.T) {
	b := NewBuffer(4096, 8<<20)
	ctx := context.Background()
	for n := 0; n < 7; n++ {
		b.Append(map[string]any{"method": "Runtime.consoleAPICalled", "params": map[string]any{"n": n, "text": strings.Repeat("汉字\\\"", 100)}})
	}
	cursor := ""
	count := 0
	for i := 0; i < 10; i++ {
		p, err := b.Read(ctx, ReadOptions{Since: cursor, MaxBytes: 2048})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := ASCIIJSON(p)
		if len(encoded) > 2048 {
			t.Fatalf("size %d", len(encoded))
		}
		count += len(p.Events)
		cursor = p.NextCursor
		if !p.HasMore {
			break
		}
	}
	if count != 7 {
		t.Fatal(count)
	}
	b.Append(map[string]any{"method": "Runtime.consoleAPICalled", "params": map[string]any{"text": strings.Repeat("汉", 30000)}})
	page, _ := b.Read(ctx, ReadOptions{Since: cursor, MaxBytes: 2048})
	if page.Events[0]["truncated"] != true {
		t.Fatal("not truncated")
	}
}
func TestEventsWaitCloseAndValidation(t *testing.T) {
	b := NewBuffer(10, 8192)
	ctx := context.Background()
	start, _ := b.Read(ctx, ReadOptions{Since: "now"})
	go func() { time.Sleep(20 * time.Millisecond); put(b, "Runtime.consoleAPICalled", 1) }()
	p, err := b.Read(ctx, ReadOptions{Since: start.NextCursor, WaitMS: 1000})
	if err != nil || len(p.Events) != 1 {
		t.Fatal(p, err)
	}
	b.Close()
	p, _ = b.Read(ctx, ReadOptions{Since: p.NextCursor, WaitMS: 1000})
	if p.ConnectionState != "closed" {
		t.Fatal(p)
	}
	for _, opts := range []ReadOptions{{Methods: []string{"bad"}}, {Limit: -1}, {MaxBytes: 20}, {WaitMS: 3001}, {Since: "bad"}} {
		if _, err = b.Read(ctx, opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
	raw, _ := ASCIIJSON(map[string]any{"a": "汉<>&", "b": 1})
	var x any
	if json.Unmarshal(raw, &x) != nil || strings.Contains(string(raw), "汉") {
		t.Fatal(string(raw))
	}
}
