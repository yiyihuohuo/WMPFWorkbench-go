package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeJumpFlagPathsUseTempDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TEMP", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TMPDIR", dir)

	targetFlag, armFlag := nativeJumpFlagPaths()
	if !strings.HasPrefix(targetFlag, dir) || !strings.HasPrefix(armFlag, dir) {
		t.Fatalf("flag paths escaped temp dir: %q %q", targetFlag, armFlag)
	}
	if err := armNativeJumpHook("wxTEST"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(targetFlag)
	if err != nil || string(raw) != "wxTEST" {
		t.Fatalf("target flag=%q err=%v", raw, err)
	}
	if _, err := os.Stat(armFlag); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Clean(dir)
}
