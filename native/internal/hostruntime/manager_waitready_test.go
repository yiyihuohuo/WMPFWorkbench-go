package hostruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitReadySession(appid string) (*Manager, *session) {
	s := &session{
		id:           "s1",
		appid:        appid,
		state:        "starting",
		ctx:          context.Background(),
		ready:        make(chan struct{}),
		readyFailed:  make(chan struct{}),
		readyChanged: make(chan struct{}, 1),
		probing:      true,
	}
	m := &Manager{rows: map[string]*session{s.id: s}}
	return m, s
}

func loginOK(s *session) {
	s.loginOK = true
	s.login = true
}

func TestWaitReadyNeedsAppStartAndSetup(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	loginOK(s)
	m.event(s, "script", map[string]any{"event": "devtools.Start", "appid": s.appid})
	m.event(s, "miniapp.setup", map[string]any{})
	out, err := m.WaitReady(context.Background(), s.id)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := out.(map[string]any)
	if !ok || row["preload_slot"] != "" {
		t.Fatalf("unexpected ready row: %#v", out)
	}
}

func TestAutoLoginHostStdoutSatisfiesLoginState(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	m.event(s, "host.stdout", map[string]any{"line": "[authcb] Login type=1 error=0 resp=000001 len=4"})
	m.event(s, "script", map[string]any{"event": "devtools.Start", "appid": s.appid})
	m.event(s, "miniapp.setup", map[string]any{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := m.WaitReady(ctx, s.id)
	if err != nil {
		t.Fatalf("AutoLogin host.stdout must satisfy loginOK: %v", err)
	}
	if row := out.(map[string]any); row["login_ok"] != true {
		t.Fatalf("login_ok not set: %#v", row)
	}
}

func TestWaitReadyRecordsPreloadSlot(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	loginOK(s)
	m.event(s, "script", map[string]any{"event": "devtools.Start", "appid": "preload-6"})
	m.event(s, "script", map[string]any{"event": "devtools.Start", "appid": s.appid})
	m.event(s, "miniapp.setup", map[string]any{})
	out, err := m.WaitReady(context.Background(), s.id)
	if err != nil {
		t.Fatal(err)
	}
	if row := out.(map[string]any); row["preload_slot"] != "preload-6" {
		t.Fatalf("preload_slot=%v", row["preload_slot"])
	}
}

func TestWaitReadyUsesCDPFallback(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	loginOK(s)
	s.websocket = "ws://127.0.0.1:62000"
	m.CDPProbe = func(ctx context.Context, ws string) bool { return ws == s.websocket }
	m.event(s, "script", map[string]any{"event": "devtools.Start", "appid": s.appid})
	out, err := m.WaitReady(context.Background(), s.id)
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)
	if row["cdp_ok"] != true || row["setup_seen"] != false {
		t.Fatalf("expected CDP fallback readiness, got %#v", row)
	}
}

func TestWaitReadyFailsOnPreload7(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	m.event(s, "script", map[string]any{"event": "devtools.Start", "appid": "preload-7"})
	_, err := m.WaitReady(context.Background(), s.id)
	var failure *ReadyFailure
	if !errors.As(err, &failure) || failure.Reason != "bad_preload" {
		t.Fatalf("expected bad_preload failure, got %v", err)
	}
}

func TestWaitReadyFailsOnRepeatedHostReset(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	for i := 0; i < 3; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	_, err := m.WaitReady(context.Background(), s.id)
	var failure *ReadyFailure
	if !errors.As(err, &failure) || failure.Reason != "repeated_reset" {
		t.Fatalf("expected repeated_reset failure, got %v", err)
	}
}

func TestHostResetAfterProgressDoesNotFailAtThree(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	loginOK(s)
	for i := 0; i < 3; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	if channelClosed(s.readyFailed) {
		t.Fatal("three resets after login progress must not fail immediately")
	}
	for i := 0; i < 2; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	if !channelClosed(s.readyFailed) {
		t.Fatal("five resets must fail even after progress")
	}
}

func TestWaitReadyWatchdogPersistsFailure(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	s.dir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.WaitReady(ctx, s.id)
	var failure *ReadyFailure
	if !errors.As(err, &failure) || failure.Reason != "watchdog" {
		t.Fatalf("expected watchdog ReadyFailure, got %v", err)
	}
	if s.state != "error" {
		t.Fatalf("session state=%s want error", s.state)
	}
	path := filepath.Join(s.dir, "observation", "failure.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("watchdog failure.json missing: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("failure.json invalid: %v", err)
	}
	reason, _ := row["failure_reason"].(string)
	if !strings.Contains(reason, "watchdog") {
		t.Fatalf("failure_reason=%q", reason)
	}
}

func TestRepeatedResetErrorIncludesDiagnostics(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	for i := 0; i < 3; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	_, err := m.WaitReady(context.Background(), s.id)
	var failure *ReadyFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected ReadyFailure, got %v", err)
	}
	if failure.Phase != "host_bootstrap" || failure.ResetCount != 3 || failure.LoginOK {
		t.Fatalf("bad diagnostics: %#v", failure)
	}
	if !strings.Contains(err.Error(), "WMPF 宿主在启动阶段 30 秒内累计重置 3 次") ||
		!strings.Contains(err.Error(), "阶段=host_bootstrap") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestRepeatedResetErrorIncludesProcessAndEventTail(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	m.event(s, "worker.started", map[string]any{"pid": float64(4321)})
	m.event(s, "script", map[string]any{"event": "installed.host"})
	for i := 0; i < 3; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	_, err := m.WaitReady(context.Background(), s.id)
	var failure *ReadyFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected ReadyFailure, got %v", err)
	}
	if failure.HostPID != 4321 || failure.Phase != "worker_started" {
		t.Fatalf("bad pid/phase diagnostics: %#v", failure)
	}
	if len(failure.LastEvents) != 5 || failure.LastEvents[0] != "worker.started" ||
		failure.LastEvents[len(failure.LastEvents)-1] != "script:host.Reset" {
		t.Fatalf("bad event tail: %#v", failure.LastEvents)
	}
	if !strings.Contains(err.Error(), "host_pid=4321") ||
		!strings.Contains(err.Error(), "最后事件=worker.started→script:installed.host→script:host.Reset→script:host.Reset→script:host.Reset") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestRepeatedResetAdviceMatchesPhase(t *testing.T) {
	early, es := waitReadySession("wx1644df8e4fe6f725")
	early.event(es, "worker.started", map[string]any{"pid": float64(4321)})
	for i := 0; i < 3; i++ {
		early.event(es, "script", map[string]any{"event": "host.Reset"})
	}
	_, earlyErr := early.WaitReady(context.Background(), es.id)
	if earlyErr == nil || !strings.Contains(earlyErr.Error(), "校验发行包完整性") ||
		strings.Contains(earlyErr.Error(), "更换目标") {
		t.Fatalf("early reset advice should target environment, got %v", earlyErr)
	}

	late, ls := waitReadySession("wx1644df8e4fe6f725")
	loginOK(ls)
	for i := 0; i < 5; i++ {
		late.event(ls, "script", map[string]any{"event": "host.Reset"})
	}
	_, lateErr := late.WaitReady(context.Background(), ls.id)
	if lateErr == nil || !strings.Contains(lateErr.Error(), "更换目标") {
		t.Fatalf("post-login reset advice should allow target change, got %v", lateErr)
	}
}

func TestWaitReadyFailsOnHostTimeout(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	m.event(s, "script", map[string]any{"event": "host.EnsureConnected.timeout"})
	_, err := m.WaitReady(context.Background(), s.id)
	var failure *ReadyFailure
	if !errors.As(err, &failure) || failure.Reason != "host_timeout" {
		t.Fatalf("expected host_timeout failure, got %v", err)
	}
}

func TestWaitReadyFailsOnLoginFailure(t *testing.T) {
	m, s := waitReadySession("wx1644df8e4fe6f725")
	m.event(s, "script", map[string]any{"event": "ilink.ThirdAppLogin.result", "error_code": float64(-2)})
	_, err := m.WaitReady(context.Background(), s.id)
	var failure *ReadyFailure
	if !errors.As(err, &failure) || failure.Reason != "login_failed" {
		t.Fatalf("expected login_failed failure, got %v", err)
	}
}
