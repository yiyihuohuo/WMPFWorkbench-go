package account

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"wmpfworkbench/internal/config"
)

func TestDumpModeSessionFromFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WXAPKG_REMOTE_HOME", home)
	// 构造最小 account.json（与 wxapkg-dump schema=2 一致）
	jsonData := `{"schema":2,"account":{"nick_name":"tester","avatar":"http://a","state":"online","openid":"owTESTOPENID1234","session":{"login_buffer":"BUF","ual":{"access_token":"TOK","open_id":"owTESTOPENID1234","refresh_token":"REF","login_type":"WX","scope":"snsapi_login,snsapi_runtime_pcsdk","expires_in":7200,"cookies":[["accesstoken","TOK"],["openid","owTESTOPENID1234"],["logintype","WX"]]}}}}`
	if err := os.WriteFile(filepath.Join(home, "account.json"), []byte(jsonData), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	_, _ = cfg.Update(context.Background(), map[string]any{"login_mode": "dump", "selected_account": "owTESTOPENID1234"})
	s, err := New(cfg).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.SDKToken != "TOK" || s.LoginBuffer != "BUF" {
		t.Fatalf("session=%+v", s)
	}
	if s.Pairs["openid"] != "owTESTOPENID1234" || s.Pairs["logintype"] != "WX" {
		t.Fatalf("pairs=%v", s.Pairs)
	}
	if s.Pairs["device_key"] != "default" {
		t.Fatalf("device_key must default to 'default' (buffer mint), got %q", s.Pairs["device_key"])
	}
}

func TestDumpModeRequestNoLegacyService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WXAPKG_REMOTE_HOME", home)
	jsonData := `{"schema":2,"account":{"openid":"owTEST","state":"online","session":{"login_buffer":"BUF","ual":{"access_token":"TOK","open_id":"owTEST"}}}}`
	if err := os.WriteFile(filepath.Join(home, "account.json"), []byte(jsonData), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	_, _ = cfg.Update(context.Background(), map[string]any{"login_mode": "dump"})
	r, err := New(cfg).Request(context.Background(), "/accounts", "GET", nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := r.([]any)
	if len(items) != 1 {
		t.Fatalf("accounts=%v", r)
	}
	refreshed, err := New(cfg).Request(context.Background(), "/accounts/refresh", "POST", map[string]any{"ref": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := refreshed.(map[string]any); !ok || m["status"] != "alive" {
		t.Fatalf("refresh=%v", refreshed)
	}
}
