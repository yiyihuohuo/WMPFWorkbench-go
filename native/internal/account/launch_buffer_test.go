package account

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"wmpfworkbench/internal/config"
)

func writeDumpAccount(t *testing.T, jsonData string) *config.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WXAPKG_REMOTE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "account.json"), []byte(jsonData), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	_, _ = cfg.Update(context.Background(), map[string]any{"login_mode": "dump"})
	return cfg
}

func TestDumpModePrefersUnconsumedLaunchBuffer(t *testing.T) {
	cfg := writeDumpAccount(t, `{"schema":2,"account":{"openid":"owTEST","state":"online","session":{"login_buffer":"REDEEMED","launch_buffer":"FRESH","ual":{"access_token":"TOK","open_id":"owTEST","refresh_token":"REF","login_type":"WX","scope":"snsapi_runtime_pcsdk"}}}}`)
	s, err := New(cfg).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.LoginBuffer != "FRESH" {
		t.Fatalf("launch must use unconsumed launch_buffer, got %q", s.LoginBuffer)
	}
}

func TestDumpModeFallsBackForOldAccounts(t *testing.T) {
	cfg := writeDumpAccount(t, `{"schema":2,"account":{"openid":"owTEST","state":"online","session":{"login_buffer":"OLD","ual":{"access_token":"TOK","open_id":"owTEST","refresh_token":"REF","login_type":"WX","scope":"snsapi_runtime_pcsdk"}}}}`)
	s, err := New(cfg).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.LoginBuffer != "OLD" {
		t.Fatalf("old account files must keep working, got %q", s.LoginBuffer)
	}
}
