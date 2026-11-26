package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"wmpfworkbench/internal/app"
)

func fixtureFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalIndexAggregatesProfilesAndRebuildRemovesStaleApps(t *testing.T) {
	root := t.TempDir()
	p, _ := app.Resolve(root)
	id := "wx0123456789abcdef"
	uid := "0123456789abcdef0123456789abcdef"
	p.Roots = []app.Profile{{Label: "one", Path: filepath.Join(root, "one")}, {Label: "two", Path: filepath.Join(root, "two")}}
	for _, r := range p.Roots {
		fixtureFile(t, filepath.Join(r.Path, "users", uid, "applet", "packages", id, "1", "code.wxapkg"), "V1MMWXabc")
	}
	fixtureFile(t, filepath.Join(p.Roots[0].Path, "users", uid, "applet", "local", id, "state.json"), `{"appid":"wx0123456789abcdef","app_name":"测试程序"}`)
	s := NewLocal(p)
	idx, err := s.Rebuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if idx.AppCount != 1 || idx.Apps[0].PackageCount != 2 || idx.Apps[0].PkgBytes != 18 || idx.Apps[0].Name == nil || *idx.Apps[0].Name != "测试程序" {
		t.Fatalf("wrong aggregate: %+v", idx)
	}
	hits, err := s.Search(context.Background(), "测试", 30)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search: %+v %v", hits, err)
	}
	// A completed rebuild is a replacement snapshot, not an accumulating cache.
	p.Roots = nil
	s = NewLocal(p)
	idx, err = s.Rebuild(context.Background())
	if err != nil || idx.AppCount != 0 {
		t.Fatalf("stale: %+v %v", idx, err)
	}
}

func TestLocalPackageDiscoveryIgnoresDirectoriesAndOtherExtensions(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	p.Roots = []app.Profile{{Label: "fixture", Path: p.Profile}}
	base := filepath.Join(p.Profile, "users", "0123456789abcdef0123456789abcdef", "applet", "packages", "wx0123456789abcdef", "1")
	fixtureFile(t, filepath.Join(base, "code.wxapkg"), "V1MMWXabc")
	fixtureFile(t, filepath.Join(base, "readme.txt"), "ignored")
	os.MkdirAll(filepath.Join(base, "directory.wxapkg"), 0700)
	rows, err := app.ListPackages(context.Background(), p, "wx0123456789abcdef")
	if err != nil || len(rows) != 1 || !rows[0].Encrypted {
		t.Fatalf("packages: %+v %v", rows, err)
	}
	if _, err = app.ListPackages(context.Background(), p, "../../escape"); err == nil {
		t.Fatal("invalid appid accepted")
	}
}
