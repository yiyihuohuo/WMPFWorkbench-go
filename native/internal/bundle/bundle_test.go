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

func TestCleanupVersionsKeepsOnlyCurrent(t *testing.T) {
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
	for _, stale := range []string{"previous", "oldest"} {
		if _, err := os.Stat(filepath.Join(root, stale)); !os.IsNotExist(err) {
			t.Fatalf("stale version %s should be removed (err=%v)", stale, err)
		}
	}
}

func TestCleanupVersionsLeavesExternalSymlinkUntouched(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	sentinel := filepath.Join(external, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "external-link")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "current"), 0700); err != nil {
		t.Fatal(err)
	}
	cleanupVersions(root, "current")
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("external target was removed: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("external link was removed: %v", err)
	}
}

func TestPathWithinRejectsOutside(t *testing.T) {
	root := t.TempDir()
	if !pathWithin(root, filepath.Join(root, "bin", "versions")) {
		t.Fatal("child path must be inside root")
	}
	if pathWithin(root, t.TempDir()) {
		t.Fatal("unrelated temp dir must be outside root")
	}
	if pathWithin(root, filepath.Dir(root)) {
		t.Fatal("parent path must be outside root")
	}
}
