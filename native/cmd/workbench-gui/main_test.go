package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/core"
)

func fixtureBackend(t *testing.T) *Backend {
	t.Helper()
	p, err := app.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := core.New(p)
	t.Cleanup(svc.Close)
	return &Backend{ctx: context.Background(), svc: svc, skipMCP: true}
}

// The GUI must expose the same frozen tools and persist toggles through the
// shared configuration, not through a frontend-only list.
func TestToolsTogglePersists(t *testing.T) {
	b := fixtureBackend(t)
	tools := b.Tools()
	if len(tools) != 18 {
		t.Fatal("expected 18 tools", len(tools))
	}
	after, err := b.SetTool("cdp_events", false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range after {
		if tool.Name == "cdp_events" {
			found = true
			if tool.Enabled {
				t.Fatal("disabled tool still enabled", tool)
			}
		}
	}
	if !found {
		t.Fatal("tool missing from catalog")
	}
	if b.svc.Config.Enabled("cdp_events") {
		t.Fatal("configuration did not persist the toggle")
	}
	after, err = b.SetTool("cdp_events", true)
	if err != nil {
		t.Fatal(err)
	}
	if !b.svc.Config.Enabled("cdp_events") {
		t.Fatal("re-enable did not persist")
	}
}

func TestSearchEmptyKeywordDoesNotHitNetwork(t *testing.T) {
	b := fixtureBackend(t)
	out, err := b.Search("  ", "online")
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	items, _ := result["items"].([]any)
	if len(items) != 0 {
		t.Fatal("empty keyword must not search", result)
	}
}

// Saving ports must persist immediately and reject out-of-range values; a
// running service is restarted by the caller path, not silently left stale.
func TestSavePortsPersistsConfig(t *testing.T) {
	b := fixtureBackend(t)
	out, err := b.SavePorts(18770, 18771, "0.0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if out["login_port"] != 18770 || out["mcp_port"] != 18771 {
		t.Fatal("ports not persisted", out)
	}
	if cfg := b.Config(); cfg["mcp_bind"] != "0.0.0.0" {
		t.Fatal("mcp_bind not persisted", cfg)
	}
	if _, err = b.SavePorts(80, 18771, "127.0.0.1"); err == nil {
		t.Fatal("out-of-range port must be rejected")
	}
	if _, err = b.SavePorts(18770, 18771, "1.2.3.4"); err == nil {
		t.Fatal("invalid mcp_bind must be rejected")
	}
	if _, err = b.SavePorts(18770, 18771, ""); err != nil {
		t.Fatal("empty mcp_bind should default to loopback", err)
	}
}

func TestAvatarRejectsEmptyRef(t *testing.T) {
	b := fixtureBackend(t)
	if src, err := b.Avatar(""); err != nil || src != "" {
		t.Fatal("empty ref must not fetch", src, err)
	}
}

func TestSessionsStartsEmpty(t *testing.T) {
	b := fixtureBackend(t)
	if rows := b.Sessions(); len(rows) != 0 {
		t.Fatal("fresh service must have no sessions", rows)
	}
}

// The clipboard entries must match the original console's field layout so MCP
// clients can parse them without adaptation.
func TestMCPConfigMatchesOriginalShape(t *testing.T) {
	stdio, err := mcpConfig("stdio", `C:\app\wmpf-gui.exe`, `C:\data`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if stdio["type"] != "stdio" || stdio["command"] != `C:\app\wmpf-gui.exe` {
		t.Fatal("stdio entry wrong", stdio)
	}
	env, _ := stdio["env"].(map[string]string)
	if env == nil || env["WMPF_HOME"] != `C:\data` || env["WMPF_AUTH_MODE"] != "managed" {
		t.Fatal("stdio env missing", stdio["env"])
	}
	args, _ := stdio["args"].([]string)
	if len(args) != 4 || args[0] != "-mcp" || args[2] != "-root" || args[3] != `C:\data` {
		t.Fatal("stdio args wrong", stdio["args"])
	}
	httpEntry, err := mcpConfig("http", "", "", map[string]any{"mcp_port": 18767, "mcp_token": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if httpEntry["type"] != "streamable-http" || httpEntry["url"] != "http://127.0.0.1:18767/mcp" {
		t.Fatal("http entry wrong", httpEntry)
	}
	headers, _ := httpEntry["headers"].(map[string]string)
	if headers == nil || headers["Authorization"] != "Bearer secret" {
		t.Fatal("http headers wrong", httpEntry["headers"])
	}
	if _, err := mcpConfig("other", "", "", nil); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
}

// The clipboard HTTP config only works when something listens on the MCP port;
// MCPStatus must reflect a live listener instead of assuming availability.
func TestMCPStatusDetectsListener(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	b := fixtureBackend(t)
	if _, err := b.svc.Config.Update(context.Background(), map[string]any{"mcp_port": number}); err != nil {
		t.Fatal(err)
	}
	status := b.MCPStatus()
	if status["running"] != true || status["url"] != "http://127.0.0.1:"+port+"/mcp" {
		t.Fatal("live listener not detected", status)
	}
}
