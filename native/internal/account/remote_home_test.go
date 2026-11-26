package account

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteHomeOverride(t *testing.T) {
	t.Setenv("WXAPKG_REMOTE_HOME", filepath.Join(t.TempDir(), "remote"))
	if got := RemoteHome(); got != os.Getenv("WXAPKG_REMOTE_HOME") {
		t.Fatalf("RemoteHome=%q", got)
	}
}

func TestMigrateDumpRemoteHome(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy", "wxapkg-dump", "remote")
	target := filepath.Join(root, "workbench", "remote")
	t.Setenv("APPDATA", filepath.Join(root, "legacy"))
	t.Setenv("WXAPKG_REMOTE_HOME", target)
	if err := os.MkdirAll(filepath.Join(legacy, "accounts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "account.json"), []byte(`{"schema":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "accounts", "owTEST.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDumpRemoteHome(target); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(target, "account.json"),
		filepath.Join(target, "accounts", "owTEST.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing migrated %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "account.json"), []byte(`{"schema":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDumpRemoteHome(target); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(target, "account.json"))
	if string(raw) != `{"schema":99}` {
		t.Fatalf("target account was overwritten: %s", raw)
	}
}
