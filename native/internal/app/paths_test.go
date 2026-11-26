package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathsDoNotDependOnWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WMPF_HOME", root)
	t.Setenv("APPDATA", "")
	t.Setenv("WMPF_DATA_ROOT", filepath.Join(root, "custom-profile"))
	p, err := Resolve("")
	if err != nil || p.Root != root || p.Profile != filepath.Join(root, "custom-profile") {
		t.Fatalf("paths: %+v %v", p, err)
	}
	uid := "0123456789abcdef0123456789abcdef"
	ap := filepath.Join(p.Profile, "users", uid, "applet")
	if err := os.MkdirAll(ap, 0700); err != nil {
		t.Fatal(err)
	}
	dirs, err := p.Applets()
	if err != nil || len(dirs) != 1 || dirs[0].Path != ap {
		t.Fatalf("applets: %+v %v", dirs, err)
	}
}
