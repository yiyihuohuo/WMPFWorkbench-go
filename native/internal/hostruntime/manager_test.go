package hostruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/bridge"
	"wmpfworkbench/internal/config"
)

func fixtureManager(t *testing.T) *Manager {
	t.Helper()
	exe := os.Getenv("WMPF_TEST_OBSERVER")
	if exe == "" {
		t.Skip("native observer fixture requires WMPF_TEST_OBSERVER")
	}
	root := t.TempDir()
	paths, _ := app.Resolve(root)
	cfg := config.New(filepath.Join(root, "config.json"))
	m := New(paths, cfg, nil)
	m.Worker = exe
	m.Host = exe
	m.fixture = true
	m.TargetAddress = "127.0.0.1:0"
	m.CDPAddress = "127.0.0.1:0"
	m.validate = func(context.Context) error { return nil }
	m.Mint = func(ctx context.Context, id, entry, dest string) (map[string]any, error) {
		if err := os.MkdirAll(dest, 0700); err != nil {
			return nil, err
		}
		os.WriteFile(filepath.Join(dest, "login.txt"), []byte("fixture-secret"), 0600)
		return map[string]any{}, nil
	}
	t.Cleanup(m.Close)
	return m
}
func TestPreflightDoesNotCreateSession(t *testing.T) {
	root := t.TempDir()
	p, _ := app.Resolve(root)
	m := New(p, config.New(filepath.Join(root, "config.json")), nil)
	if _, err := m.Launch(context.Background(), "wx0123456789abcdef", "", 60, nil); err == nil {
		t.Fatal("missing executable passed")
	}
	if len(m.Sessions()) != 0 {
		t.Fatal("invalid launch created session")
	}
	if _, err := os.Stat(filepath.Join(p.Data, "desktop", "sessions")); !os.IsNotExist(err) {
		t.Fatal("invalid launch wrote session directory")
	}
}

func TestValidateSessionSeconds(t *testing.T) {
	cases := []struct {
		seconds int
		wantErr bool
	}{
		{seconds: 0},
		{seconds: 60},
		{seconds: 1800},
		{seconds: 1801},
		{seconds: 7200},
		{seconds: -1, wantErr: true},
		{seconds: 1, wantErr: true},
		{seconds: 59, wantErr: true},
	}
	for _, tc := range cases {
		err := ValidateSessionSeconds(tc.seconds)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ValidateSessionSeconds(%d) error=%v, wantErr=%v", tc.seconds, err, tc.wantErr)
		}
	}
}

func TestSessionTimeoutChannel(t *testing.T) {
	if ch, stop := sessionTimeoutChannel(0); ch != nil {
		stop()
		t.Fatal("unlimited session must not have a timeout channel")
	}
	ch, stop := sessionTimeoutChannel(1801)
	defer stop()
	if ch == nil {
		t.Fatal("explicit duration above former cap must have a timeout channel")
	}
}
func TestFixtureSingleSessionAndBoundedClose(t *testing.T) {
	m := fixtureManager(t)
	value, err := m.Launch(context.Background(), "wx0123456789abcdef", "", 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := value.(map[string]any)
	id := row["session_id"].(string)
	if row["hide_window"] != true {
		t.Fatal("default hidden not applied")
	}
	if _, err = m.Launch(context.Background(), "wx0123456789abcdef", "", 60, nil); err == nil {
		t.Fatal("concurrent session accepted")
	}
	until := time.Now().Add(15 * time.Second)
	for {
		rows := m.Sessions()
		if len(rows) > 0 && rows[0]["worker_started"] == true {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("worker not running: %v", rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := m.CloseSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	closed := result.(map[string]any)
	if closed["ok"] != true || closed["state"] != "closed" {
		t.Fatal(closed)
	}
	if closed["debug_connected"] == true {
		t.Fatal("fixture must not claim CDP readiness")
	}
	if _, err = os.Stat(filepath.Join(m.Paths.Data, "desktop", "sessions", id, "inputs", "login.txt")); !os.IsNotExist(err) {
		t.Fatal("ticket remains")
	}
	if closed["cleanup_verified"] != true {
		raw, _ := os.ReadFile(filepath.Join(m.Paths.Data, "desktop", "sessions", id, "observation", "events.jsonl"))
		t.Log(string(raw))
		t.Fatal("cleanup not verified", closed)
	}
	if closed["cleanup_state"] != "verified" {
		t.Fatal("cleanup state not reported", closed)
	}
	hide := false
	next, err := m.Launch(context.Background(), "wx0123456789abcdef", "", 60, &hide)
	if err != nil {
		t.Fatal(err)
	}
	if next.(map[string]any)["hide_window"] != false {
		t.Fatal("explicit visible ignored")
	}
}
func TestCloseCancelsTicketRequest(t *testing.T) {
	m := fixtureManager(t)
	started := make(chan struct{})
	m.Mint = func(ctx context.Context, _, _, _ string) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	value, err := m.Launch(context.Background(), "wx0123456789abcdef", "", 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := m.CloseSession(ctx, value.(map[string]any)["session_id"].(string))
	if err != nil || result.(map[string]any)["ok"] != true {
		t.Fatal(result, err)
	}
}
func TestValidateRuntimeRejectsDifferentBytes(t *testing.T) {
	p := t.TempDir()
	os.MkdirAll(filepath.Join(p, "runtime"), 0700)
	os.WriteFile(filepath.Join(p, "runtime", "flue.dll"), []byte("wrong runtime"), 0600)
	if err := ValidateRuntime(context.Background(), p); err == nil || !strings.Contains(err.Error(), "Runtime") {
		t.Fatal("unverified runtime accepted", err)
	}
}

func TestValidateRuntimeMissingGivesActionableError(t *testing.T) {
	p := t.TempDir()
	err := ValidateRuntime(context.Background(), p)
	if err == nil {
		t.Fatal("empty runtime dir must be rejected before launch")
	}
	msg := err.Error()
	for _, want := range []string{"flue.dll", "WeChatAppEx.exe", "应用宝", "runtime\\5.10.7300.414"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing actionable detail %q in: %s", want, msg)
		}
	}
}

func TestSetupAloneDoesNotClaimDebugReady(t *testing.T) {
	m := fixtureManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &session{state: "running", websocket: "ws://127.0.0.1:1", ctx: ctx}
	m.event(s, "miniapp.setup", nil)
	if m.public(s)["debug_connected"] == true {
		t.Fatal("setup without evaluate is not debug readiness")
	}
}

// Close() must not return before run() can finish its worst-case stop path:
// cleanup verdict (4s) + owned.Stop (4s) + scanner drain (2s).
func TestCloseBudgetCoversInternalWaits(t *testing.T) {
	if closeBudget < 10*time.Second {
		t.Fatal("close budget does not cover internal waits", closeBudget)
	}
}

func TestDebugReadyDowngradesOnDisconnect(t *testing.T) {
	root := t.TempDir()
	p, _ := app.Resolve(root)
	m := New(p, config.New(filepath.Join(root, "config.json")), nil)
	s := &session{state: "running", debug: true, created: time.Now(), ctx: context.Background(), done: make(chan struct{}), cleanupDone: make(chan struct{})}
	if m.public(s)["state"] != "debug_ready" {
		t.Fatal("fixture did not start in debug_ready", m.public(s))
	}
	m.event(s, "miniapp.disconnected", nil)
	if m.public(s)["state"] == "debug_ready" || m.public(s)["debug_connected"] == true {
		t.Fatal("debug_ready survived upstream disconnect", m.public(s))
	}
}

func TestProcessInventoryFromObserverEvents(t *testing.T) {
	root := t.TempDir()
	p, _ := app.Resolve(root)
	m := New(p, config.New(filepath.Join(root, "config.json")), nil)
	s := &session{id: "s1", appid: "wx0123456789abcdef", state: "running", created: time.Now(), ctx: context.Background(), done: make(chan struct{}), cleanupDone: make(chan struct{})}
	m.rows["s1"] = s
	m.event(s, "process.seen", map[string]any{"pid": float64(4242)})
	m.event(s, "process.seen", map[string]any{"pid": 4242})
	m.event(s, "process.seen", map[string]any{"pid": float64(4243)})
	rows := m.ProcessInventory()
	if len(rows) != 2 {
		t.Fatal("observer pids not collected or not deduplicated", rows)
	}
	if rows[0]["pid"] != 4242 || rows[0]["session_id"] != "s1" || rows[0]["source"] != "observer" {
		t.Fatal("inventory row missing provenance", rows[0])
	}
}

// ok=true only means the close flow finished. native cleanup is a separate,
// three-valued fact: verified / failed / unconfirmed. A missing log line must
// never be reported as verified, and cleanup.failed must not be swallowed.
func TestCleanupStateSemantics(t *testing.T) {
	root := t.TempDir()
	p, _ := app.Resolve(root)
	m := New(p, config.New(filepath.Join(root, "config.json")), nil)
	newSession := func() *session {
		return &session{state: "stopping", created: time.Now(), ctx: context.Background(), done: make(chan struct{}), cleanupDone: make(chan struct{})}
	}
	verified := newSession()
	m.event(verified, "cleanup.verified", nil)
	if m.public(verified)["cleanup_state"] != "verified" || m.public(verified)["cleanup_verified"] != true {
		t.Fatal("verified cleanup not reported", m.public(verified))
	}
	failed := newSession()
	m.event(failed, "cleanup.failed", nil)
	if m.public(failed)["cleanup_state"] != "failed" || m.public(failed)["cleanup_verified"] != false {
		t.Fatal("failed cleanup reported as verified", m.public(failed))
	}
	if failed.message == "" {
		t.Fatal("failed cleanup has no diagnostic message")
	}
	unknown := newSession()
	if m.public(unknown)["cleanup_state"] != "unconfirmed" || m.public(unknown)["cleanup_verified"] != false {
		t.Fatal("missing cleanup signal must stay unconfirmed", m.public(unknown))
	}
	select {
	case <-verified.cleanupDone:
	default:
		t.Fatal("verified did not release cleanup waiters")
	}
	select {
	case <-failed.cleanupDone:
	default:
		t.Fatal("failed did not release cleanup waiters")
	}
}

// Every bridge event must survive on disk, including events the state machine
// ignores. Without this, a missing console event cannot be attributed to the
// upstream, the bridge, the client pump or the buffer.
func TestBridgeDiagnosticsPersisted(t *testing.T) {
	root := t.TempDir()
	p, _ := app.Resolve(root)
	m := New(p, config.New(filepath.Join(root, "config.json")), nil)
	m.TargetAddress = "127.0.0.1:0"
	m.CDPAddress = "127.0.0.1:0"
	s := &session{id: "diag", appid: "wx0123456789abcdef", dir: filepath.Join(p.Data, "desktop", "sessions", "diag"), state: "running", created: time.Now(), ctx: context.Background(), done: make(chan struct{})}
	if err := os.MkdirAll(filepath.Join(s.dir, "observation"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, blog, err := m.startBridge(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+b.TargetAddress(), nil)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := bridge.Encode(1, "mysteryCategory", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, "observation", "bridge.jsonl")
	deadline := time.Now().Add(3 * time.Second)
	var raw []byte
	for {
		raw, _ = os.ReadFile(path)
		if bytes.Contains(raw, []byte("miniapp.category")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge diagnostics not persisted: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bytes.Contains(raw, []byte("bridge.ready")) {
		t.Fatal("bridge.ready not persisted", string(raw))
	}
	ws.Close()
	b.Close()
	blog.Close()
	raw, _ = os.ReadFile(path)
	if !bytes.Contains(raw, []byte("bridge.stats")) {
		t.Fatal("final stats not persisted", string(raw))
	}
}

// A -10 rejection must trigger exactly one ticket refresh through the login
// service; normal launches never call this path.
func TestRefreshTicketCallsLoginService(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/accounts/refresh" {
			calls++
			w.Write([]byte(`{"code":0,"data":{"status":"alive"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p, _ := app.Resolve(t.TempDir())
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	if _, err := cfg.Update(context.Background(), map[string]any{"login_mode": "legacy", "login_url": srv.URL, "login_token": "fixture", "selected_account": "acct"}); err != nil {
		t.Fatal(err)
	}
	m := New(p, cfg, account.New(cfg))
	if err := m.refreshTicket(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("ticket refresh not called", calls)
	}
}

func TestRefreshTicketRequiresAliveStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"status":"expired"}}`))
	}))
	defer srv.Close()
	p, _ := app.Resolve(t.TempDir())
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	if _, err := cfg.Update(context.Background(), map[string]any{"login_mode": "legacy", "login_url": srv.URL, "login_token": "fixture", "selected_account": "acct"}); err != nil {
		t.Fatal(err)
	}
	m := New(p, cfg, account.New(cfg))
	if err := m.refreshTicket(context.Background()); err == nil || !strings.Contains(err.Error(), "未确认有效") {
		t.Fatalf("non-alive account refresh must block retry, got %v", err)
	}
}

func TestAttemptPathsIsolateRetryInputsAndGate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session")
	first := pathsForAttempt(root, 1)
	second := pathsForAttempt(root, 2)
	if first.inputs == second.inputs || first.gate == second.gate || first.workerConfig == second.workerConfig {
		t.Fatalf("retry reused first attempt paths: first=%+v second=%+v", first, second)
	}
	if first.inputs != filepath.Join(root, "inputs") || second.inputs != filepath.Join(root, "attempt-2", "inputs") {
		t.Fatalf("unexpected attempt input paths: first=%+v second=%+v", first, second)
	}
}

func TestRecordFailurePersistsReason(t *testing.T) {
	root := t.TempDir()
	p, _ := app.Resolve(root)
	m := New(p, config.New(filepath.Join(root, "config.json")), nil)
	s := &session{id: "d0a98feb14f9544f", appid: "wxde8ac0a21135c07d", dir: filepath.Join(root, "session"),
		state: "error", message: "启动服务拒绝请求", created: time.Now()}
	m.recordFailure(s, errors.New("early mint failure"))
	path := filepath.Join(s.dir, "observation", "failure.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	if row["event"] != "session.failed" || row["session_id"] != s.id || row["appid"] != s.appid {
		t.Fatalf("failure record missing identity: %v", row)
	}
	reason := fmt.Sprint(row["reason"])
	if !strings.Contains(reason, "启动服务拒绝请求") || !strings.Contains(reason, "early mint failure") {
		t.Fatalf("reason lost detail: %s", reason)
	}
}

func TestFailureReasonDedupes(t *testing.T) {
	s := &session{message: "启动服务 HTTP 403"}
	if got := failureReason(s, errors.New("启动服务 HTTP 403")); got != "启动服务 HTTP 403" {
		t.Fatalf("duplicate reason not deduped: %s", got)
	}
	s2 := &session{message: "账号登录失败（error_code=-10）"}
	got := failureReason(s2, errors.New("票据刷新失败：网络超时"))
	if !strings.Contains(got, "error_code=-10") || !strings.Contains(got, "网络超时") {
		t.Fatalf("distinct reasons not joined: %s", got)
	}
}

func readyFailureSession(dir string) (*Manager, *session) {
	s := &session{
		id: "fail-1", appid: "wx0123456789abcdef", dir: dir,
		state: "running", created: time.Now(), ctx: context.Background(),
		done: make(chan struct{}), cleanupDone: make(chan struct{}),
		ready: make(chan struct{}), readyFailed: make(chan struct{}),
		readyChanged: make(chan struct{}, 1),
	}
	return &Manager{rows: map[string]*session{s.id: s}}, s
}

func TestFailReadyFirstCauseWins(t *testing.T) {
	m, s := readyFailureSession(t.TempDir())
	m.failReady(s, "repeated_reset", "count=3")
	first, firstMessage := s.readyErr, s.message
	m.failReady(s, "host_timeout", "later event")
	if !channelClosed(s.readyFailed) {
		t.Fatal("readyFailed not signalled")
	}
	if s.readyErr != first || s.readyFailReason != "repeated_reset" {
		t.Fatalf("later failure overwrote first cause: %#v / %s", s.readyErr, s.readyFailReason)
	}
	failure, ok := s.readyErr.(*ReadyFailure)
	if !ok || failure.Reason != "repeated_reset" || failure.Detail != "count=3" {
		t.Fatalf("first ReadyFailure mutated: %#v", s.readyErr)
	}
	if s.message == "" || s.message != firstMessage {
		t.Fatalf("session message missing or overwritten: %q", s.message)
	}
}

func TestFailReadyPersistsFailureJSON(t *testing.T) {
	dir := t.TempDir()
	m, s := readyFailureSession(dir)
	m.event(s, "worker.started", map[string]any{"pid": float64(800), "worker_pid": float64(700)})
	for i := 0; i < 3; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	path := filepath.Join(dir, "observation", "failure.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"event": "session.failed", "session_id": s.id, "appid": s.appid,
		"failure_reason": "repeated_reset", "phase": "worker_started",
		"reset_count": float64(3), "recent_reset_count": float64(3),
		"host_pid": float64(800), "initial_host_pid": float64(800),
		"worker_pid": float64(700), "worker_started": true,
	} {
		if row[key] != want {
			t.Fatalf("%s=%v, want %v in %s", key, row[key], want, raw)
		}
	}
	for _, key := range []string{"reason", "last_events", "login_confirmed", "launch_confirmed", "debug_connected", "cleanup_state", "failed_at"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("failure.json missing %s: %s", key, raw)
		}
	}
	events, ok := row["last_events"].([]any)
	if !ok || len(events) < 3 {
		t.Fatalf("last_events not persisted: %v", row["last_events"])
	}
	if reason := fmt.Sprint(row["reason"]); !strings.Contains(reason, "30 秒内累计重置 3 次（其中 10 秒内 3 次）") {
		t.Fatalf("ReadyFailure reason not preferred: %s", reason)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("atomic write left temp file: %v", err)
	}
}

func TestHostRebuildPhaseAndProgress(t *testing.T) {
	dir := t.TempDir()
	m, s := readyFailureSession(dir)
	m.event(s, "script", map[string]any{"event": "host.ReLaunch", "pid": float64(9100)})
	for _, inner := range []string{"host.InitCore", "host.ReBind", "host.LaunchAndBind"} {
		m.event(s, "script", map[string]any{"event": inner})
	}
	if !s.hostRebindSeen || failurePhase(s) != "host_rebind" {
		t.Fatalf("rebuild chain not recognised: rebind=%v phase=%s", s.hostRebindSeen, failurePhase(s))
	}
	for i := 0; i < 3; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	if channelClosed(s.readyFailed) {
		t.Fatal("rebuild progress must raise the three-reset threshold")
	}
	for i := 0; i < 2; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	if !channelClosed(s.readyFailed) {
		t.Fatal("five resets must fail even after rebuild progress")
	}
	failure, ok := s.readyErr.(*ReadyFailure)
	if !ok || failure.Phase != "host_rebind" || !failure.HostRebindSeen {
		t.Fatalf("failure lost rebuild phase: %#v", s.readyErr)
	}
	if strings.Contains(failure.Error(), "尚未进入登录") {
		t.Fatalf("rebuild phase still claims no login attempt: %s", failure.Error())
	}
	if s.hostPID != 9100 || s.initialHostPID != 9100 {
		t.Fatalf("ReLaunch pid not adopted: current=%d initial=%d", s.hostPID, s.initialHostPID)
	}
}

func TestHostPIDUpdatesPreserveInitial(t *testing.T) {
	m, s := readyFailureSession(t.TempDir())
	m.event(s, "worker.started", map[string]any{"pid": float64(1001), "worker_pid": float64(1002)})
	if s.hostPID != 1001 || s.initialHostPID != 1001 || s.workerPID != 1002 {
		t.Fatalf("initial pids not recorded: host=%d initial=%d worker=%d", s.hostPID, s.initialHostPID, s.workerPID)
	}
	m.event(s, "process.seen", map[string]any{"pid": float64(2001)})
	if s.hostPID != 2001 || s.initialHostPID != 1001 {
		t.Fatalf("process.seen did not update current host pid: host=%d initial=%d", s.hostPID, s.initialHostPID)
	}
	m.event(s, "script", map[string]any{"event": "host.ReLaunch", "payload": map[string]any{"host_pid": float64(3001)}})
	if s.hostPID != 3001 || s.initialHostPID != 1001 {
		t.Fatalf("ReLaunch did not adopt new host pid: host=%d initial=%d", s.hostPID, s.initialHostPID)
	}
}

func TestRepeatedResetErrorUsesTimeWindows(t *testing.T) {
	m, s := readyFailureSession(t.TempDir())
	for i := 0; i < 3; i++ {
		m.event(s, "script", map[string]any{"event": "host.Reset"})
	}
	failure, ok := s.readyErr.(*ReadyFailure)
	if !ok || failure.ResetCount != 3 || failure.RecentResetCount != 3 {
		t.Fatalf("reset counts not retained: %#v", s.readyErr)
	}
	msg := failure.Error()
	if !strings.Contains(msg, "30 秒内累计重置 3 次（其中 10 秒内 3 次）") || strings.Contains(msg, "连续重置") {
		t.Fatalf("error text still uses misleading wording: %s", msg)
	}
}

func TestRecordFailureReportsWriteFailure(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	logs := []string{}
	m := &Manager{Logf: func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}}
	s := &session{id: "fail-log", dir: blocked, state: "error", message: "boom"}
	m.recordFailure(s, nil)
	if len(logs) == 0 || !strings.Contains(logs[0], "失败记录目录创建失败") {
		t.Fatalf("write failure not reported via Logf: %v", logs)
	}
}

func TestRunDeferKeepsExistingReadyFailure(t *testing.T) {
	m, s := readyFailureSession(t.TempDir())
	m.MintLaunch = func(ctx context.Context, id, entry, dest string) (map[string]any, error) {
		return nil, nil
	}
	s.resetCount, s.recentResetCount = 3, 3
	m.failReady(s, "repeated_reset", "count=3")
	s.state = "stopping"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.run(ctx, s)
	if s.state != "error" {
		t.Fatalf("ReadyFailure overwritten as %s", s.state)
	}
	if s.readyFailReason != "repeated_reset" || !strings.Contains(s.message, "30 秒内累计重置 3 次") {
		t.Fatalf("ReadyFailure reason or message lost: %s / %q", s.readyFailReason, s.message)
	}
}
