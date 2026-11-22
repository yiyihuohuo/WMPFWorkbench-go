package config

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type Values map[string]any
type Store struct{ Path string }

func New(path string) *Store { return &Store{Path: path} }

func Defaults() Values {
	return Values{
		"disabled_tools": []string{}, "selected_account": "", "login_url": "", "login_token": "",
		"mcp_port": 18767, "login_port": 18766, "mcp_token": "", "mcp_enabled": true,
		"mcp_bind": "127.0.0.1",
		// Hide the mini-program host window by default: launching a debugging
		// session should not pop a visible WMPF host window on the desktop.
		"auth_mode": "managed", "auto_start": true, "hide_miniapp_window": true, "tool_catalog_version": 3,
		// wxapkg-dump sidecar: login + arbitrary mini-program package export.
		"login_mode": "dump", "dump_port": 8788, "dump_export_dir": "", "dump_exe": "", "dump_workspace": "", "dump_admin_key": "",
	}
}

var replacements = map[string]string{
	"search_local_mini_programs": "search_mini_programs", "rebuild_index": "index", "browse_indexed": "index", "index_stats": "index", "list_packages": "index",
	"host_status": "runtime_status", "runtime_sessions": "runtime_status", "list_running": "runtime_status",
	"cdp_enable": "cdp_send", "cdp_get_script_source": "cdp_scripts",
	"netlog_find_existing": "netlog", "netlog_summarize": "netlog", "netlog_api_surface": "netlog",
}
var retired = map[string]bool{"source_export": true, "dump_source": true, "dump_source_via_cdp": true,
	"sdk_abi_notes": true, "xweb_relay_contract": true, "netlog_trigger_spec": true}

func normalize(raw Values) (Values, error) {
	out := Defaults()
	for k, v := range raw {
		out[k] = v
	}
	for _, key := range []string{"mcp_enabled", "hide_miniapp_window"} {
		if _, ok := out[key].(bool); !ok {
			return nil, fmt.Errorf("invalid boolean %s", key)
		}
	}
	names := []string{}
	switch v := out["disabled_tools"].(type) {
	case []string:
		names = v
	case []any:
		for _, n := range v {
			s, ok := n.(string)
			if !ok {
				return nil, errors.New("invalid disabled_tools")
			}
			names = append(names, s)
		}
	default:
		return nil, errors.New("invalid disabled_tools")
	}
	version := 3
	if raw["tool_catalog_version"] == nil {
		version = 1
	} else {
		switch v := raw["tool_catalog_version"].(type) {
		case float64:
			version = int(v)
		case int:
			version = v
		default:
			return nil, errors.New("invalid catalog version")
		}
	}
	if version < 3 {
		seen := map[string]bool{}
		for _, n := range names {
			if retired[n] {
				continue
			}
			if v, ok := replacements[n]; ok {
				n = v
			}
			seen[n] = true
		}
		names = []string{}
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		out["tool_catalog_version"] = 3
	}
	out["disabled_tools"] = names
	return out, nil
}

func (s *Store) Read() (Values, error) {
	f, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return nil, errors.New("控制台配置无法读取")
	}
	defer f.Close()
	var raw Values
	decoder := json.NewDecoder(io.LimitReader(f, 4<<20))
	if decoder.Decode(&raw) != nil || raw == nil {
		return nil, errors.New("控制台配置无法读取")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("控制台配置无法读取")
	}
	return normalize(raw)
}

func (s *Store) Enabled(name string) bool {
	cfg, err := s.Read()
	if err != nil || cfg["mcp_enabled"] != true {
		return false
	}
	for _, n := range cfg["disabled_tools"].([]string) {
		if n == name {
			return false
		}
	}
	return true
}

func (s *Store) Update(ctx context.Context, changes map[string]any) (Values, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return nil, err
	}
	unlock, err := lockFile(ctx, s.Path+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, err := s.Read()
	if err != nil {
		return nil, err
	}
	for k, v := range changes {
		cfg[k] = v
	}
	cfg, err = normalize(cfg)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".config-*")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.Path)
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// EnsureToken returns the token stored under key, generating and persisting a
// random one on first use. The read-modify-write runs under the same file lock
// as Update, so concurrent callers (GUI login service startup vs. account
// client on first refresh) always observe a single shared token instead of
// each minting a different one and invalidating each other's requests.
func (s *Store) EnsureToken(ctx context.Context, key string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return "", err
	}
	unlock, err := lockFile(ctx, s.Path+".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	cfg, err := s.Read()
	if err != nil {
		return "", err
	}
	if token := String(cfg, key); token != "" {
		return token, nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	cfg[key] = token
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".config-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.Path)
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func String(v Values, key string) string { s, _ := v[key].(string); return s }
func Int(v Values, key string) int {
	switch n := v[key].(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}
