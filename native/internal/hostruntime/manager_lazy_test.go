package hostruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManualLoginMarkerTriggersLazyMint(t *testing.T) {
	dir := t.TempDir()
	s := &session{
		id:           "s-lazy",
		appid:        "wx1644df8e4fe6f725",
		state:        "starting",
		ctx:          context.Background(),
		ready:        make(chan struct{}),
		readyFailed:  make(chan struct{}),
		readyChanged: make(chan struct{}, 1),
		inputsDir:    dir,
		probing:      true,
	}
	m := &Manager{rows: map[string]*session{s.id: s}}
	done := make(chan struct{})
	m.MintLoginBuffer = func(ctx context.Context, target string, fresh bool) error {
		if target != dir || !fresh {
			t.Errorf("unexpected mint args target=%q fresh=%v", target, fresh)
		}
		if err := os.WriteFile(filepath.Join(target, "login.txt"), []byte("ticket"), 0600); err != nil {
			return err
		}
		close(done)
		return nil
	}
	m.event(s, "host.stdout", map[string]any{"line": "[login] auth_state=manual_login_required requesting login buffer"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lazy mint was not triggered")
	}
	data, err := os.ReadFile(filepath.Join(dir, "login.txt"))
	if err != nil || string(data) != "ticket" {
		t.Fatalf("login.txt=%q err=%v", string(data), err)
	}
}

func TestPublicKeepsCDPWebsocketAcrossDebugFlips(t *testing.T) {
	s := &session{id: "s-cdp", appid: "wx1644df8e4fe6f725", state: "running", websocket: "ws://127.0.0.1:62000"}
	m := &Manager{rows: map[string]*session{s.id: s}}
	row := m.public(s)
	if row["cdp_websocket"] != s.websocket {
		t.Fatalf("cdp_websocket=%v, want %q", row["cdp_websocket"], s.websocket)
	}
	if row["debug_connected"] != false {
		t.Fatalf("debug_connected=%v, want false", row["debug_connected"])
	}
}

func TestTicketRejectionNoLongerForcesRefresh(t *testing.T) {
	if ticketRejected(-10) || ticketRejected(33) {
		t.Fatal("ticket rejection must not blindly request refresh")
	}
}
