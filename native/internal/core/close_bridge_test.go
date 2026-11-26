package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCloseBridgeSessionWritesLauncherAppID(t *testing.T) {
	s := &Service{}
	sid := "test-close-bridge-session"
	launcher := "wxBRIDGE"
	path := filepath.Join(os.TempDir(), "wmpf-close-bridge-"+sid+".txt")
	defer os.Remove(path)
	s.closeBridgeSession(sid, launcher)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != launcher {
		t.Fatalf("close file=%q, want %q", raw, launcher)
	}
}
