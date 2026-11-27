package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureVersionedBundleDoesNotOverwriteLegacy(t *testing.T) {
	dataDir := t.TempDir()
	legacy := filepath.Join(dataDir, "bin", "wxapkg-dump.exe")
	if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("old-running-process"), 0700); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{
		"wxapkg-dump.exe": []byte("new-sidecar"),
		"helper.exe":      []byte("new-helper"),
	}
	paths, err := ensureVersionedBundle(dataDir, files)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(paths["wxapkg-dump.exe"], filepath.Join("bin", "versions")) {
		t.Fatalf("not versioned: %s", paths["wxapkg-dump.exe"])
	}
	if got, err := os.ReadFile(legacy); err != nil || string(got) != "old-running-process" {
		t.Fatalf("legacy binary was overwritten: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(paths["wxapkg-dump.exe"]); err != nil || string(got) != "new-sidecar" {
		t.Fatalf("versioned binary mismatch: %q err=%v", got, err)
	}

	again, err := ensureVersionedBundle(dataDir, files)
	if err != nil {
		t.Fatal(err)
	}
	if again["wxapkg-dump.exe"] != paths["wxapkg-dump.exe"] {
		t.Fatalf("version path changed: %s != %s", again["wxapkg-dump.exe"], paths["wxapkg-dump.exe"])
	}
}

func TestCleanupVersionsKeepsCurrentAndOnePrevious(t *testing.T) {
	root := t.TempDir()
	names := []string{"oldest", "previous", "current"}
	now := time.Now()
	for i, name := range names {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		mod := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(dir, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	cleanupVersions(root, "current")
	if _, err := os.Stat(filepath.Join(root, "current")); err != nil {
		t.Fatal("current version removed")
	}
	if _, err := os.Stat(filepath.Join(root, "previous")); err != nil {
		t.Fatal("previous version should be kept for rollback")
	}
	if _, err := os.Stat(filepath.Join(root, "oldest")); !os.IsNotExist(err) {
		t.Fatal("oldest version should be removed")
	}
}
