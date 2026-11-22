package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/loginsvc"
)

type inventoryRuntime struct {
	rows []map[string]any
}

func (r *inventoryRuntime) Launch(context.Context, string, string, int, *bool) (any, error) {
	return nil, nil
}
func (r *inventoryRuntime) CloseSession(context.Context, string) (any, error) { return nil, nil }
func (r *inventoryRuntime) Sessions() []map[string]any                        { return nil }
func (r *inventoryRuntime) Close()                                            {}
func (r *inventoryRuntime) ProcessInventory() []map[string]any                { return r.rows }

func TestRuntimeStatusProcessInventory(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	s := &Service{Paths: p, Runtime: &inventoryRuntime{rows: []map[string]any{{"pid": 1234, "session_id": "s1"}}}}
	out, err := s.Call(context.Background(), "runtime_status", map[string]any{"include_processes": true})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	rows, _ := result["processes"].([]map[string]any)
	if len(rows) != 1 || rows[0]["pid"] != 1234 {
		t.Fatal("runtime_status did not return native inventory", result)
	}
	if strings.Contains(fmt.Sprint(result["diagnostics_note"]), "not yet connected") {
		t.Fatal("stale placeholder note", result)
	}
}

// login_status must distinguish "service not running" from "no accounts"
// instead of a bare error string, without exposing the desktop token.
func TestLoginStatusReportsServiceState(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	if _, err := cfg.Update(context.Background(), map[string]any{"login_url": "http://127.0.0.1:1", "login_port": float64(1), "login_token": "fixture", "selected_account": "acct", "login_mode": "legacy"}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Paths: p, Config: cfg, Account: account.New(cfg), LoginSvc: loginsvc.New(p, cfg)}
	out, err := s.Call(context.Background(), "login_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	service, ok := result["login_service"].(loginsvc.Status)
	if !ok {
		t.Fatal("missing login service state", result)
	}
	if service.Running || service.URL != "http://127.0.0.1:1" {
		t.Fatal("unexpected service state", service)
	}
	if result["error"] == nil {
		t.Fatal("missing service must be reported", result)
	}
	if strings.Contains(fmt.Sprint(result), "fixture") {
		t.Fatal("token must not be exposed", result)
	}
}
