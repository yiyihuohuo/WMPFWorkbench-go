// Package bundle embeds the native helpers so the desktop app and MCP server
// can ship as self-contained executables. The 67 MB Frida worker is the reason
// the binaries are embedded at build time instead of living in Git.
package bundle

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed bin/*
var files embed.FS

// Ensure writes embedded helpers under <data>/bin/versions/<version> and
// returns their paths. Versioned paths mean a running sidecar is never
// overwritten in place; the next launch simply uses the new directory.
func Ensure(dataDir string) (map[string]string, error) {
	entries, err := files.ReadDir("bin")
	if err != nil {
		return nil, err
	}
	rawFiles := map[string][]byte{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".exe") {
			continue
		}
		raw, err := files.ReadFile("bin/" + name)
		if err != nil {
			return nil, err
		}
		rawFiles[name] = raw
	}
	return ensureVersionedBundle(dataDir, rawFiles)
}

func ensureVersionedBundle(dataDir string, rawFiles map[string][]byte) (map[string]string, error) {
	if len(rawFiles) == 0 {
		return map[string]string{}, nil
	}
	version := bundleVersion(rawFiles)
	root := filepath.Join(dataDir, "bin", "versions")
	target := filepath.Join(root, version)
	if err := os.MkdirAll(target, 0700); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rawFiles))
	for name, raw := range rawFiles {
		dst, err := ensureVersionedFile(target, name, raw)
		if err != nil {
			return nil, fmt.Errorf("释放 %s 失败：%w", name, err)
		}
		out[name] = dst
	}
	cleanupVersions(root, version)
	return out, nil
}

func bundleVersion(rawFiles map[string][]byte) string {
	names := make([]string, 0, len(rawFiles))
	for name := range rawFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		sum := sha256.Sum256(rawFiles[name])
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func ensureVersionedFile(dir, name string, raw []byte) (string, error) {
	dst := filepath.Join(dir, name)
	if current, err := os.ReadFile(dst); err == nil && bytes.Equal(current, raw) {
		return dst, nil
	}
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0700); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		// Windows cannot replace an existing file by rename. Only a stale
		// same-version file can be here; running executables are locked and
		// will fail the removal rather than being overwritten.
		if removeErr := os.Remove(dst); removeErr != nil {
			return "", err
		}
		if err := os.Rename(tmpName, dst); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// cleanupVersions keeps the active version and one previous version for
// rollback. Locked directories are left alone; a later launch retries cleanup.
func cleanupVersions(root, current string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type versionDir struct {
		name string
		mod  time.Time
	}
	dirs := make([]versionDir, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == current {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, versionDir{name: entry.Name(), mod: info.ModTime()})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod.After(dirs[j].mod) })
	for i := 1; i < len(dirs); i++ {
		_ = os.RemoveAll(filepath.Join(root, dirs[i].name))
	}
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
