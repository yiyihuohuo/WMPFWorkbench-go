// Package bundle embeds the native helpers so the desktop app and MCP server
// can ship as self-contained executables. The 67 MB Frida worker is the reason
// the binaries are embedded at build time instead of living in Git.
package bundle

import (
	"bytes"
	"embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed bin/*
var files embed.FS

// Ensure writes embedded helpers under <data>/bin and returns their paths.
// Unchanged files are left alone so restarts stay cheap.
func Ensure(dataDir string) (map[string]string, error) {
	target := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(target, 0700); err != nil {
		return nil, err
	}
	entries, err := files.ReadDir("bin")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".exe") {
			continue
		}
		raw, err := files.ReadFile("bin/" + name)
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(target, name)
		if current, err := os.ReadFile(dst); err == nil && bytes.Equal(current, raw) {
			out[name] = dst
			continue
		}
		if err := os.WriteFile(dst, raw, 0700); err != nil {
			return nil, err
		}
		out[name] = dst
	}
	return out, nil
}

// Apply sets the environment variables consumed by hostruntime and wxapkg.
func Apply(paths map[string]string) {
	if v := paths["wmpf-observer.exe"]; v != "" {
		os.Setenv("WMPF_OBSERVER_PATH", v)
	}
	if v := paths["wmpfhost_integrated.exe"]; v != "" {
		os.Setenv("WMPF_HOST_PATH", v)
	}
	if v := paths["wxapkg-offline.exe"]; v != "" {
		os.Setenv("WMPF_WXAPKG_TOOL", v)
	}
	if v := paths["wxapkg-dump.exe"]; v != "" {
		os.Setenv("WXAPKG_DUMP_EXE", v)
	}
}
