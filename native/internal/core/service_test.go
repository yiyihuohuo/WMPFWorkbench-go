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

type launchRecordingRuntime struct {
	seconds []int
}

func (r *launchRecordingRuntime) Launch(_ context.Context, _, _ string, seconds int, _ *bool) (any, error) {
	r.seconds = append(r.seconds, seconds)
	return map[string]any{"ok": true}, nil
}
func (*launchRecordingRuntime) CloseSession(context.Context, string) (any, error) { return nil, nil }
func (*launchRecordingRuntime) Sessions() []map[string]any                       { return nil }
func (*launchRecordingRuntime) Close()                                           {}

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

func TestLaunchDurationIsOptionalAndUnbounded(t *testing.T) {
	runtime := &launchRecordingRuntime{}
	s := &Service{Runtime: runtime}
	cases := []struct {
		name    string
		args    map[string]any
		seconds int
		wantErr bool
	}{
		{name: "omitted means unlimited", args: map[string]any{"appid": "wx0123456789abcdef"}, seconds: 0},
		{name: "zero means unlimited", args: map[string]any{"appid": "wx0123456789abcdef", "seconds": 0}, seconds: 0},
		{name: "explicit standard duration", args: map[string]any{"appid": "wx0123456789abcdef", "seconds": 60}, seconds: 60},
		{name: "duration above former cap", args: map[string]any{"appid": "wx0123456789abcdef", "seconds": 7200}, seconds: 7200},
		{name: "short positive duration rejected", args: map[string]any{"appid": "wx0123456789abcdef", "seconds": 59}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Call(context.Background(), "launch", tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected invalid duration to be rejected")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := runtime.seconds[len(runtime.seconds)-1]; got != tc.seconds {
				t.Fatalf("Launch received seconds=%d, want %d", got, tc.seconds)
			}
		})
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
