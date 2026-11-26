package hostruntime

import "testing"

func TestValidAppid(t *testing.T) {
	cases := map[string]bool{
		"wx1644df8e4fe6f725": true,
		"wx7af53044e50b4c0e": true,
		"preload-abc":        false,
		"invalid":            false,
		"other":              false,
		"wx1644DF8E4FE6F725": false, // uppercase hex is not a real appid
		"wx1644df8e4fe6f72":  false, // too short
	}
	for in, want := range cases {
		if got := validAppid(in); got != want {
			t.Fatalf("validAppid(%q)=%v want %v", in, got, want)
		}
	}
}

func TestActiveAppidFollowsInAppJump(t *testing.T) {
	m := &Manager{rows: map[string]*session{}}
	s := &session{id: "s1", appid: "wx1644df8e4fe6f725", state: "running", websocket: "ws://127.0.0.1:62000"}

	// Before any jump the public view reports the launched appid.
	if pub := m.public(s); pub["active_appid"] != s.appid || pub["jumped"] != false {
		t.Fatalf("pre-jump view wrong: %v", pub)
	}

	// The observer reports the jumped-to applet through devtools.Start.
	m.event(s, "script", map[string]any{"event": "devtools.Start", "appid": "wx7af53044e50b4c0e"})
	if s.activeAppid != "wx7af53044e50b4c0e" {
		t.Fatalf("activeAppid=%q", s.activeAppid)
	}
	pub := m.public(s)
	if pub["active_appid"] != "wx7af53044e50b4c0e" || pub["jumped"] != true || pub["appid"] != s.appid {
		t.Fatalf("post-jump view wrong: %v", pub)
	}

	// A preload sentinel must not overwrite the real appid.
	m.event(s, "script", map[string]any{"event": "runtime.ReusePreload", "appid": "preload-x"})
	if s.activeAppid != "wx7af53044e50b4c0e" {
		t.Fatalf("sentinel overwrote appid: %q", s.activeAppid)
	}
}

func TestSetupMarksDebugReady(t *testing.T) {
	m := &Manager{rows: map[string]*session{}}
	s := &session{id: "s2", appid: "wx1644df8e4fe6f725", state: "running", websocket: "ws://127.0.0.1:1"}
	m.event(s, "miniapp.setup", map[string]any{})
	if !s.debug {
		t.Fatal("setupContext must mark the debug channel ready")
	}
	if pub := m.public(s); pub["cdp_websocket"] != s.websocket {
		t.Fatalf("debug-ready session must expose a websocket: %v", pub)
	}
	m.event(s, "miniapp.disconnected", map[string]any{})
	if s.debug {
		t.Fatal("disconnect must clear debug")
	}
}
