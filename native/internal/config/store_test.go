package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDefaultsMigrationAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := New(path)
	cfg, err := s.Read()
	if err != nil || cfg["hide_miniapp_window"] != true {
		t.Fatalf("defaults must hide host window: %v %v", cfg, err)
	}
	raw := `{"tool_catalog_version":2,"custom":"keep","disabled_tools":["source_export","cdp_send"],"hide_miniapp_window":false}`
	if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["custom"] != "keep" || cfg["hide_miniapp_window"] != false || s.Enabled("cdp_send") {
		t.Fatalf("explicit hide=false must survive migration: %v", cfg)
	}
	if len(cfg["disabled_tools"].([]string)) != 1 {
		t.Fatal(cfg)
	}
	if _, err = s.Update(context.Background(), map[string]any{"selected_account": "fixture"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Read()
	if cfg["custom"] != "keep" {
		t.Fatal("lost unknown field")
	}
}

func TestInvalidConfigFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := New(path)
	for _, raw := range []string{`broken`, `[]`, `{"mcp_enabled":"yes"}`, `{"disabled_tools":[1]}`} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, err := s.Read(); err == nil || s.Enabled("cdp_events") {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestConcurrentUpdatesPreserveOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			if _, err := New(path).Update(context.Background(), map[string]any{key: key}); err != nil {
				t.Error(err)
			}
		}(key)
	}
	wg.Wait()
	raw, _ := os.ReadFile(path)
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil || cfg["a"] != "a" || cfg["d"] != "d" {
		t.Fatalf("lost update %s", raw)
	}
}
