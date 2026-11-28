package dumpclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The sidecar path must never be a hardcoded absolute path: it ships inside the
// bundle and is extracted next to the app. These tests pin the resolution rules.
func TestResolveExePrefersEnvOverride(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "custom-wxapkg-dump.exe")
	if err := os.WriteFile(want, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WXAPKG_DUMP_EXE", want)
	got, searched := resolveExe()
	if got != want || len(searched) != 0 {
		t.Fatalf("resolveExe() = (%q, %v), want (%q, nil)", got, searched, want)
	}
}

func TestResolveExeFindsCwdCopy(t *testing.T) {
	dir := t.TempDir()
	copyPath := filepath.Join(dir, exeName)
	if err := os.WriteFile(copyPath, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WXAPKG_DUMP_EXE", "")
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	got, _ := resolveExe()
	if filepath.Base(got) != exeName {
		t.Fatalf("resolveExe() = %q, want a path ending in %s", got, exeName)
	}
	if st, err := os.Stat(got); err != nil || st.IsDir() {
		t.Fatalf("resolved path is not a file: %q (%v)", got, err)
	}
}

func TestResolveExeReportsSearchedLocations(t *testing.T) {
	t.Setenv("WXAPKG_DUMP_EXE", "")
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	got, searched := resolveExe()
	if got != exeName {
		t.Fatalf("missing binary should return bare %q, got %q", exeName, got)
	}
	if len(searched) == 0 {
		t.Fatal("expected the searched locations to be reported for the error message")
	}
	for _, p := range searched {
		if !strings.Contains(strings.ToLower(p), strings.ToLower(exeName)) {
			t.Fatalf("searched entry should reference %s: %q", exeName, p)
		}
	}
}

func TestResolveExeIgnoresInvalidEnvAndUsesLatestVersion(t *testing.T) {
	dataDir := t.TempDir()
	versionsDir := filepath.Join(dataDir, "bin", "versions")
	older := filepath.Join(versionsDir, "aaa", exeName)
	newer := filepath.Join(versionsDir, "bbb", exeName)
	for _, path := range []string{older, newer} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("stub"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WXAPKG_DUMP_EXE", filepath.Join(dataDir, "missing.exe"))
	got, _ := resolveExe(dataDir)
	if got != newer {
		t.Fatalf("resolveExe() = %q, want newest version %q", got, newer)
	}
}

func TestResolveExeRejectsEnvDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WXAPKG_DUMP_EXE", dir)
	got, searched := resolveExe()
	if isExistingFile(got) && got == dir {
		t.Fatal("a directory must not be accepted as the sidecar binary")
	}
	found := false
	for _, entry := range searched {
		if entry == dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("invalid env path should be reported in searched list: %v", searched)
	}
}

func TestEnsureErrorIsActionable(t *testing.T) {
	t.Setenv("WXAPKG_DUMP_EXE", "")
	c := New(Options{BaseURL: "http://127.0.0.1:1", AutoStart: true})
	c.opts.ExePath = filepath.Join(t.TempDir(), "does-not-exist.exe")
	_, _ = resolveExe()
	err := c.Ensure(t.Context())
	if err == nil {
		t.Fatal("expected an error when the sidecar binary is missing")
	}
	msg := err.Error()
	if !strings.Contains(msg, exeName) || !strings.Contains(msg, "WXAPKG_DUMP_EXE") {
		t.Fatalf("error must name the binary and the env var escape hatch: %s", msg)
	}
}
