// Package hostruntime orchestrates the owned C++ host and native Frida worker.
package hostruntime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/bridge"
	"wmpfworkbench/internal/cdp"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/launch"
	"wmpfworkbench/internal/process"
)

type MintFunc func(context.Context, string, string, string) (map[string]any, error)
type MintLoginBufferFunc func(context.Context, string, bool) error
type Manager struct {
	Paths                                   app.Paths
	Config                                  *config.Store
	Account                                 *account.Client
	Worker, Host, TargetAddress, CDPAddress string
	Mint                                    MintFunc
	MintFresh                               MintFunc
	MintLaunch                              MintFunc
	MintLoginBuffer                         MintLoginBufferFunc
	// Logf, when set, receives lifecycle failures (including early mint
	// failures that never reach the observer's events.jsonl). Nil-safe.
	Logf func(string, ...any)
	// CDPProbe verifies that the owned CDP websocket can actually answer a
	// Runtime.evaluate request. Tests can replace it; production uses the
	// default websocket probe.
	CDPProbe func(context.Context, string) bool
	// OnSetup runs after a mini program setupContext. The service uses it to
	// re-apply matching userscripts when a new runtime/context is created.
	OnSetup  func(context.Context, string)
	mu       sync.Mutex
	rows     map[string]*session
	closing  bool
	fixture  bool
	validate func(context.Context) error
}
type session struct {
	id, appid, entry, dir, state, message string
	// activeAppid is the applet currently rendered by the host. It differs from
	// appid when the launched mini program jumps to another one in-app: that
	// target is loaded by the runtime itself, so it never went through the
	// app-store launch path and is invisible to session bookkeeping unless we
	// track the appid reported by the runtime's own devtools events.
	activeAppid                                    string
	seconds                                        int
	created                                        time.Time
	hide, login, launched, debug, started, cleanup bool
	websocket, target                              string
	ctx                                            context.Context
	probing                                        bool
	cancel                                         context.CancelFunc
	done                                           chan struct{}
	cleanupState                                   string
	attemptErrorCode                               int
	cleanupDone                                    chan struct{}
	cleanupOnce                                    sync.Once
	pids                                           []int
	ready, readyFailed, readyChanged               chan struct{}
	readyOnce, readyFailOnce                       sync.Once
	readyErr                                       error
	readyFailReason                                string
	appStartSeen, setupSeen, loginOK, cdpOK        bool
	hostConnected                                  bool
	lastLoginCode                                  int
	preloadSlot                                    string
	inputsDir                                      string
	manualMintStarted                              bool
	resetCount                                     int
}

func New(p app.Paths, cfg *config.Store, a *account.Client) *Manager {
	worker := filepath.Join(p.Root, "build", "wmpf-observer.exe")
	if v := os.Getenv("WMPF_OBSERVER_PATH"); v != "" {
		worker = v
	}
	host := filepath.Join(p.Root, "hostcpp", "wmpfhost_integrated.exe")
	if v := os.Getenv("WMPF_HOST_PATH"); v != "" {
		host = v
	}
	values, _ := cfg.Read()
	targetPort := config.Int(values, "target_port")
	if targetPort < 1 || targetPort > 65535 {
		targetPort = 9421
	}
	cdpPort := config.Int(values, "cdp_port")
	if cdpPort < 1 || cdpPort > 65535 {
		cdpPort = 62000
	}
	m := &Manager{Paths: p, Config: cfg, Account: a, Worker: worker, Host: host, TargetAddress: fmt.Sprintf("127.0.0.1:%d", targetPort), CDPAddress: fmt.Sprintf("127.0.0.1:%d", cdpPort), rows: map[string]*session{}}
	m.Mint = launch.New(a).Mint
	m.MintFresh = launch.New(a).MintFresh
	m.MintLaunch = launch.New(a).MintLaunch
	m.MintLoginBuffer = launch.New(a).MintLoginBuffer
	m.validate = func(ctx context.Context) error { return ValidateRuntime(ctx, p.Runtime) }
	return m
}
func terminal(s *session) bool { return s.state == "closed" || s.state == "error" }

// ValidateSessionSeconds treats zero as an unlimited session. Explicit time
// limits retain a short-duration guard but have no application-level maximum.
func ValidateSessionSeconds(seconds int) error {
	if seconds < 0 || (seconds > 0 && seconds < 60) {
		return errors.New("seconds 必须为 0（不限制）或至少 60 秒")
	}
	return nil
}

// sessionTimeoutChannel returns nil for an unlimited session. Long explicit
// durations are waited in time.Duration-sized chunks to avoid overflow.
func sessionTimeoutChannel(seconds int) (<-chan time.Time, func()) {
	if seconds <= 0 {
		return nil, func() {}
	}
	out := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		remaining := uint64(seconds) + 15 // allow the worker its cleanup window
		const maxChunk = uint64(1<<63-1) / uint64(time.Second)
		for remaining > 0 {
			chunk := remaining
			if chunk > maxChunk {
				chunk = maxChunk
			}
			timer := time.NewTimer(time.Duration(chunk) * time.Second)
			select {
			case fired := <-timer.C:
				remaining -= chunk
				if remaining == 0 {
					out <- fired
					return
				}
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
		}
	}()
	return out, cancel
}

// validAppid reports whether s looks like a real mini-program appid
// ("wx" + 16 lowercase hex). Preload placeholders such as "preload-xxx" and
// the observer's "invalid"/"other" sentinels must be ignored.
func validAppid(s string) bool {
	if len(s) != 18 || !strings.HasPrefix(s, "wx") {
		return false
	}
	for _, c := range s[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func (m *Manager) public(s *session) map[string]any {
	state := s.state
	if state == "running" && s.debug {
		state = "debug_ready"
	}
	var ws any
	// The websocket is the stable bridge port, not the current page target.
	// Keep it visible while the session is running so cdp_connect can retry
	// after an in-app jump replaces the old runtime target.
	if s.state == "running" && s.websocket != "" {
		ws = s.websocket
	}
	cleanupState := s.cleanupState
	if cleanupState == "" {
		cleanupState = "unconfirmed"
	}
	active := s.activeAppid
	if active == "" {
		active = s.appid
	}
	return map[string]any{"session_id": s.id, "appid": s.appid, "active_appid": active, "jumped": active != "" && active != s.appid, "preload_slot": s.preloadSlot, "host_connected": s.hostConnected, "login_ok": s.loginOK, "app_start_seen": s.appStartSeen, "setup_seen": s.setupSeen, "cdp_ok": s.cdpOK, "ready_failure": s.readyFailReason, "state": state, "created_at": float64(s.created.UnixMilli()) / 1000, "seconds": s.seconds, "time_limit_enabled": s.seconds > 0, "hide_window": s.hide, "error": s.message, "login_confirmed": s.login, "launch_confirmed": s.launched, "debug_connected": s.debug && s.state == "running", "worker_started": s.started, "cleanup_verified": s.cleanup, "cleanup_state": cleanupState, "cdp_websocket": ws}
}
func (m *Manager) Sessions() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for _, s := range m.rows {
		out = append(out, m.public(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["created_at"].(float64) < out[j]["created_at"].(float64) })
	return out
}

// ProcessInventory reports pids the native observer explicitly took ownership
// of (process.seen events), with provenance. It is not a system-wide process
// listing and must not be presented as one.
func (m *Manager) ProcessInventory() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for _, s := range m.rows {
		for _, pid := range s.pids {
			out = append(out, map[string]any{"pid": pid, "session_id": s.id, "appid": s.appid, "state": s.state, "source": "observer"})
		}
	}
	return out
}

func (m *Manager) preflight(ctx context.Context, id, entry string, seconds int) error {
	if !app.AppID.MatchString(id) || len(entry) > 4096 {
		return errors.New("appid 或 path 不合法")
	}
	if err := ValidateSessionSeconds(seconds); err != nil {
		return err
	}
	for _, exe := range []string{m.Worker, m.Host} {
		st, err := os.Stat(exe)
		if err != nil || !st.Mode().IsRegular() {
			return errors.New("原生 worker 或自产宿主未构建")
		}
	}
	if err := m.validate(ctx); err != nil {
		return err
	}
	if !m.fixture && entry != "" {
		if st, err := os.Stat(entry); err != nil || !st.Mode().IsRegular() {
			return errors.New("显式指定的包文件不存在")
		}
	}
	return nil
}
func (m *Manager) Launch(ctx context.Context, id, entry string, seconds int, hide *bool) (any, error) {
	if err := m.preflight(ctx, id, entry, seconds); err != nil {
		return nil, err
	}
	values, err := m.Config.Read()
	if err != nil {
		return nil, err
	}
	hidden, _ := values["hide_miniapp_window"].(bool)
	if hide != nil {
		hidden = *hide
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, errors.New("程序正在关闭")
	}
	for _, s := range m.rows {
		if !terminal(s) {
			return nil, errors.New("当前调试桥仅支持一个会话，请先关闭已有会话")
		}
	}
	// The OS lock also serializes independent stdio/HTTP service instances.
	if err = os.MkdirAll(m.Paths.Data, 0700); err != nil {
		return nil, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	release, err := config.AcquireLock(lockCtx, filepath.Join(m.Paths.Data, "native-runtime.lock"))
	cancel()
	if err != nil {
		return nil, errors.New("其他服务实例已有活动会话")
	}
	// Do not kill WeChatAppEx processes here: the GUI owns the preload pool and
	// typed utility children. Port checks plus the single-session lock already
	// reject stale bridges; killing runtime children causes host.ReBind loops.
	for _, addr := range []string{m.TargetAddress, m.CDPAddress} {
		probe, e := net.Listen("tcp", addr)
		if e != nil {
			release()
			return nil, errors.New("调试端口已占用")
		}
		probe.Close()
	}
	var key [8]byte
	if _, err = rand.Read(key[:]); err != nil {
		release()
		return nil, err
	}
	ident := hex.EncodeToString(key[:])
	dir := filepath.Join(m.Paths.Data, "desktop", "sessions", ident)
	if err = os.MkdirAll(filepath.Join(dir, "observation"), 0700); err != nil {
		release()
		return nil, err
	}
	runCtx, stop := context.WithCancel(context.Background())
	s := &session{id: ident, appid: id, entry: entry, seconds: seconds, hide: hidden, created: time.Now(), dir: dir, state: "starting", ctx: runCtx, cancel: stop, done: make(chan struct{}), cleanupDone: make(chan struct{}), ready: make(chan struct{}), readyFailed: make(chan struct{}), readyChanged: make(chan struct{}, 1)}
	m.rows[ident] = s
	// Bound in-memory terminal history; session artifacts remain available locally.
	old := []*session{}
	for _, r := range m.rows {
		if terminal(r) {
			old = append(old, r)
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].created.Before(old[j].created) })
	for len(old) > 5 {
		delete(m.rows, old[0].id)
		old = old[1:]
	}
	go func() { defer release(); m.run(runCtx, s) }()
	return m.public(s), nil
}
func (m *Manager) signalCleanup(s *session) {
	if s.cleanupDone == nil {
		return
	}
	s.cleanupOnce.Do(func() { close(s.cleanupDone) })
}

// ReadyFailure is the typed readiness error used by callers to decide whether
// a launch should be restarted and to report a concrete reason.
type ReadyFailure struct {
	Reason        string
	Detail        string
	PreloadSlot   string
	Phase         string
	ResetCount    int
	LoginOK       bool
	HostConnected bool
	AppStartSeen  bool
	SetupSeen     bool
	LastLoginCode int
}

func failurePhase(s *session) string {
	if s == nil {
		return "unknown"
	}
	switch {
	case s.setupSeen || s.cdpOK:
		return "setup"
	case s.appStartSeen:
		return "app_start"
	case s.loginOK:
		return "login_ready"
	case s.hostConnected:
		return "host_connected"
	case s.started:
		return "worker_started"
	default:
		return "host_bootstrap"
	}
}

func (e *ReadyFailure) Error() string {
	if e == nil {
		return "未知启动失败"
	}
	parts := []string{e.Reason}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	if e.PreloadSlot != "" {
		parts = append(parts, "preload="+e.PreloadSlot)
	}
	return strings.Join(parts, ": ")
}
func channelClosed(ch chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
func (m *Manager) notifyReadyLocked(s *session) {
	if s.readyChanged == nil {
		return
	}
	select {
	case s.readyChanged <- struct{}{}:
	default:
	}
}
func (m *Manager) signalReady(s *session) {
	if s == nil || s.ready == nil {
		return
	}
	if !s.loginOK || !s.appStartSeen || (!s.setupSeen && !s.cdpOK) {
		return
	}
	s.readyOnce.Do(func() { close(s.ready) })
	m.notifyReadyLocked(s)
}
func (m *Manager) failReady(s *session, reason string, detail ...string) {
	if s == nil || s.readyFailed == nil || channelClosed(s.ready) {
		return
	}
	text := strings.Join(detail, " ")
	s.readyFailReason = reason
	s.readyErr = &ReadyFailure{
		Reason: reason, Detail: text, PreloadSlot: s.preloadSlot,
		Phase: failurePhase(s), ResetCount: s.resetCount,
		LoginOK: s.loginOK, HostConnected: s.hostConnected,
		AppStartSeen: s.appStartSeen, SetupSeen: s.setupSeen || s.cdpOK,
		LastLoginCode: s.lastLoginCode,
	}
	s.readyFailOnce.Do(func() { close(s.readyFailed) })
	m.notifyReadyLocked(s)
}
func (m *Manager) noteStartEvent(s *session, appid string) {
	if s == nil || appid == "" {
		return
	}
	if strings.HasPrefix(appid, "preload-") {
		if s.preloadSlot != appid {
			s.preloadSlot = appid
			m.notifyReadyLocked(s)
		}
		if appid == "preload-7" && !s.appStartSeen {
			m.failReady(s, "bad_preload", "启动落入 preload-7")
		}
		return
	}
	if appid == s.appid && !s.appStartSeen {
		s.appStartSeen = true
		m.signalReady(s)
		m.notifyReadyLocked(s)
	}
}
func defaultCDPProbe(ctx context.Context, ws string) bool {
	if ws == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := cdp.Dial(probeCtx, ws)
	if err != nil {
		return false
	}
	defer conn.Close()
	raw, err := conn.Send(probeCtx, "Runtime.evaluate", map[string]any{"expression": "1+1", "returnByValue": true}, "")
	if err != nil {
		return false
	}
	var response struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return false
	}
	switch v := response.Result.Value.(type) {
	case float64:
		return v == 2
	case int:
		return v == 2
	case string:
		return v == "2"
	default:
		return false
	}
}
func (m *Manager) probeCDP(ctx context.Context, ws string) bool {
	probe := m.CDPProbe
	if probe == nil {
		probe = defaultCDPProbe
	}
	return probe(ctx, ws)
}

// requestManualLoginBufferLocked starts the single lazy ticket mint after the
// host reports that WMPF requires manual login. It must be called with m.mu held.
func (m *Manager) requestManualLoginBufferLocked(s *session) {
	if s == nil || s.manualMintStarted || s.inputsDir == "" {
		return
	}
	if m.MintLoginBuffer == nil {
		m.failReady(s, "manual_login_failed", "缺少按需 mint 登录票据的启动器")
		return
	}
	s.manualMintStarted = true
	dir := s.inputsDir
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
		defer cancel()
		if err := m.MintLoginBuffer(ctx, dir, true); err != nil {
			m.mu.Lock()
			s.message = "按需登录票据获取失败：" + err.Error()
			m.failReady(s, "manual_login_failed", s.message)
			m.mu.Unlock()
			return
		}
		m.mu.Lock()
		s.message = ""
		m.mu.Unlock()
	}()
}

// WaitReady blocks until the event stream proves that the launched appid has
// a devtools.Start plus either miniapp.setup or a working CDP evaluate. It
// intentionally does not use fixed sleeps for success; ctx is only a safety
// watchdog and concrete failure events produce typed ReadyFailure reasons.
func (m *Manager) WaitReady(ctx context.Context, id string) (any, error) {
	m.mu.Lock()
	s := m.rows[id]
	if s == nil {
		m.mu.Unlock()
		return nil, errors.New("找不到此会话")
	}
	ready, failed, changed := s.ready, s.readyFailed, s.readyChanged
	m.mu.Unlock()
	if ready == nil || failed == nil || changed == nil {
		return nil, errors.New("会话没有事件等待通道")
	}
	for {
		m.mu.Lock()
		if channelClosed(ready) {
			out := m.public(s)
			m.mu.Unlock()
			return out, nil
		}
		if channelClosed(failed) {
			err := s.readyErr
			m.mu.Unlock()
			if err == nil {
				err = errors.New("未知启动事件失败")
			}
			return nil, err
		}
		ws := s.websocket
		loginOK, appStartSeen, cdpOK := s.loginOK, s.appStartSeen, s.cdpOK
		m.mu.Unlock()

		if loginOK && appStartSeen && !cdpOK && ws != "" && m.probeCDP(ctx, ws) {
			m.mu.Lock()
			if !s.cdpOK {
				s.cdpOK = true
				s.debug = true
			}
			m.signalReady(s)
			m.mu.Unlock()
			continue
		}

		m.mu.Lock()
		if channelClosed(ready) {
			out := m.public(s)
			m.mu.Unlock()
			return out, nil
		}
		if channelClosed(failed) {
			err := s.readyErr
			m.mu.Unlock()
			if err == nil {
				err = errors.New("未知启动事件失败")
			}
			return nil, err
		}
		ready, failed, changed = s.ready, s.readyFailed, s.readyChanged
		slot := s.preloadSlot
		m.mu.Unlock()
		select {
		case <-ready:
		case <-failed:
		case <-changed:
		case <-ctx.Done():
			return nil, &ReadyFailure{Reason: "watchdog", Detail: ctx.Err().Error(), PreloadSlot: slot}
		}
	}
}

func (m *Manager) event(s *session, event string, payload map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if terminal(s) {
		return
	}
	switch event {
	case "worker.started":
		s.started = true
		if s.state != "stopping" {
			s.state = "running"
		}
	case "login.confirmed":
		s.login = true
		s.loginOK = true
		s.message = ""
		m.notifyReadyLocked(s)
	case "miniapp.connected":
		if !s.hostConnected {
			s.hostConnected = true
			m.notifyReadyLocked(s)
		}
	case "host.applet_event":
		if payload["code"] == float64(0) {
			s.launched = true
		}
	case "miniapp.setup":
		// setupContext means the applet's devtools channel is live. Treat that
		// as debug-ready instead of waiting for an evaluate round-trip: in-app
		// jumps replace the applet (and its runtime) mid-session, and the old
		// "1+1 == 2" gate kept debug_connected false for those sessions even
		// though the channel worked. probe is kept only to refine the appid.
		s.setupSeen = true
		m.signalReady(s)
		m.notifyReadyLocked(s)
		s.debug = true
		if m.OnSetup != nil {
			appid := s.activeAppid
			if appid == "" {
				appid = s.appid
			}
			callback := m.OnSetup
			go callback(context.Background(), appid)
		}
		if !s.probing {
			s.probing = true
			go m.probe(s)
		}
	case "miniapp.disconnected":
		// The upstream target is gone; a successful earlier evaluate probe is
		// no longer proof of a live debugging connection.
		s.debug = false
		s.cdpOK = false
		m.notifyReadyLocked(s)
	case "process.seen":
		var pid int
		switch v := payload["pid"].(type) {
		case float64:
			pid = int(v)
		case int:
			pid = v
		}
		if pid > 0 {
			for _, existing := range s.pids {
				if existing == pid {
					return
				}
			}
			s.pids = append(s.pids, pid)
		}
	case "cleanup.verified":
		s.cleanup = true
		s.cleanupState = "verified"
		m.signalCleanup(s)
	case "cleanup.failed":
		s.cleanup = false
		s.cleanupState = "failed"
		if s.message == "" {
			s.message = "原生进程清理核验失败，详见 observation/events.jsonl"
		}
		m.signalCleanup(s)
	case "host.stdout":
		line, _ := payload["line"].(string)
		if strings.Contains(line, "auth_state=manual_login_required") {
			m.requestManualLoginBufferLocked(s)
		}
		// Official login flow can succeed through AutoLogin without ever
		// producing ilink.ThirdAppLogin.result. Accept the typed host callback
		// and the post-AutoLogin GetUserInfo result as authoritative login state.
		if strings.Contains(line, "[authcb] Login") && strings.Contains(line, "error=0") ||
			strings.Contains(line, "[authcb] GetUserInfo status=0") ||
			(strings.Contains(line, "[login] GetUserInfo(after AutoLogin") && strings.Contains(line, "recovered=1")) {
			if !s.loginOK {
				s.loginOK = true
				s.login = true
				s.message = ""
				m.notifyReadyLocked(s)
			}
		}
	case "script":
		// Frida payloads arrive wrapped as {"event":"script","payload":{"event":...}};
		// the runtime's login verdict lives in the inner event, not the top level.
		// ilink.ThirdAppLogin (logged_in=0) is the normal pre-login state and must
		// not set an error; only .result with a non-zero code is a failure.
		if inner, ok := payload["event"].(string); ok {
			switch inner {
			case "ilink.ThirdAppLogin.result":
				if code, ok := payload["error_code"].(float64); ok {
					s.lastLoginCode = int(code)
					if code == 0 {
						s.loginOK = true
						s.login = true
						s.message = ""
						m.notifyReadyLocked(s)
					} else if code != -10 {
						s.attemptErrorCode = int(code)
						s.message = fmt.Sprintf("账号登录失败（error_code=%v）", int(code))
						m.failReady(s, "login_failed", s.message)
					}
				}
			case "host.EnsureConnected.timeout":
				m.failReady(s, "host_timeout", inner)
			case "host.Reset":
				s.resetCount++
				if s.resetCount >= 3 {
					m.failReady(s, "repeated_reset", fmt.Sprintf("count=%d", s.resetCount))
				}
			case "devtools.Start", "devtools.SetupContext", "runtime.Construct", "runtime.ReusePreload":
				// The observer reads the runtime's appid straight out of memory
				// (runtimeInfo). An in-app jump to a mini program that is not in
				// the app store shows up here as a new appid while the session
				// stays alive — that is the only reliable signal we have for it.
				if id, ok := payload["appid"].(string); ok {
					m.noteStartEvent(s, id)
					if validAppid(id) && s.activeAppid != id {
						s.activeAppid = id
						if m.Logf != nil {
							m.Logf("会话 %s 当前小程序切换为 %s", s.id, id)
						}
					}
				}
			}
		}
	case "host.EnsureConnected.timeout":
		m.failReady(s, "host_timeout", event)
	case "host.Reset":
		s.resetCount++
		if s.resetCount >= 3 {
			m.failReady(s, "repeated_reset", fmt.Sprintf("count=%d", s.resetCount))
		}
	case "script.error", "host.spawn_error", "host.job_error", "session.attach_error", "script.create_error", "script.load_error":
		s.message = "原生观测或宿主启动失败: " + event
		m.failReady(s, "observer_failed", s.message)
	}
}

// appidExpr asks the applet for its own appid. It is only a fallback: the
// observer already reports the appid from runtime memory, which works even when
// this expression is unavailable (e.g. games with a restricted globals surface).
const appidExpr = `(function(){try{if(typeof wx!=='undefined'&&wx.getAccountInfoSync){var a=wx.getAccountInfoSync();if(a&&a.miniProgram&&a.miniProgram.appId)return a.miniProgram.appId}}catch(e){}try{if(typeof __wxConfig!=='undefined'&&__wxConfig.appId)return __wxConfig.appId}catch(e){}return ''})()`

func (m *Manager) probe(s *session) {
	m.mu.Lock()
	address, ctx := s.websocket, s.ctx
	m.mu.Unlock()
	if ctx == nil {
		return
	}
	// Keep probing for the whole session. An in-app jump replaces the old page
	// target; debug_connected must be restored by the next successful evaluate
	// instead of staying false forever after miniapp.disconnected.
	for ctx.Err() == nil {
		m.mu.Lock()
		done := terminal(s)
		m.mu.Unlock()
		if done {
			break
		}
		attempt, c := context.WithTimeout(ctx, 3*time.Second)
		conn, err := cdp.Dial(attempt, address)
		if err == nil {
			raw, e := conn.Send(attempt, "Runtime.evaluate", map[string]any{"expression": appidExpr, "returnByValue": true}, "")
			conn.Close()
			if e == nil {
				var r struct {
					Result struct {
						Value any `json:"value"`
					} `json:"result"`
				}
				if json.Unmarshal(raw, &r) == nil {
					if id, ok := r.Result.Value.(string); ok && validAppid(id) {
						m.mu.Lock()
						if s.activeAppid != id {
							s.activeAppid = id
						}
						s.debug = true
						s.cdpOK = true
						m.notifyReadyLocked(s)
						m.mu.Unlock()
					}
				}
			}
		}
		c()
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	m.mu.Lock()
	s.probing = false
	m.mu.Unlock()
}

// startBridge owns the diagnostic log and the private-protocol bridge. Every
// callback is persisted before it reaches the state machine, so unhandled
// events remain available for post-mortem analysis.
func (m *Manager) startBridge(ctx context.Context, s *session) (*bridge.Server, *eventLog, error) {
	blog, err := newEventLog(filepath.Join(s.dir, "observation", "bridge.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	b, err := bridge.Start(ctx, m.TargetAddress, m.CDPAddress, func(e string, p map[string]any) {
		blog.Log(e, p)
		m.event(s, e, p)
	})
	if err != nil {
		blog.Close()
		return nil, nil, err
	}
	m.mu.Lock()
	s.websocket = "ws://" + b.CDPAddress()
	s.target = "ws://" + b.TargetAddress()
	m.mu.Unlock()
	return b, blog, nil
}

func (m *Manager) run(ctx context.Context, s *session) {
	var runErr error
	defer func() {
		os.Remove(filepath.Join(s.dir, "inputs", "login.txt"))
		os.Remove(filepath.Join(s.dir, "attempt-2", "inputs", "login.txt"))
		m.mu.Lock()
		defer m.mu.Unlock()
		if s.state == "stopping" {
			s.state = "closed"
		} else if s.state != "error" && runErr != nil {
			s.state = "error"
			if s.message == "" {
				s.message = runErr.Error()
			}
		} else if s.state != "error" {
			s.state = "closed"
		}
		// Persist terminal failures so the reason survives process restarts and
		// in-memory history eviction. Early mint failures happen before the
		// bridge/worker start, so observation/bridge.jsonl and events.jsonl do
		// not exist in that case — this file is the only post-mortem evidence.
		if s.state == "error" {
			m.recordFailure(s, runErr)
			if !channelClosed(s.ready) {
				m.failReady(s, "session_error", failureReason(s, runErr))
			}
			if m.Logf != nil {
				m.Logf("会话 %s (%s) 启动失败：%s", s.id, s.appid, failureReason(s, runErr))
			}
		}
		close(s.done)
	}()
	for attempt := 0; attempt < 2; attempt++ {
		m.mu.Lock()
		s.attemptErrorCode = 0
		if attempt > 0 {
			s.state = "starting"
			s.message = ""
		}
		m.mu.Unlock()
		retry, err := m.attempt(ctx, s, attempt+1, attempt == 1)
		runErr = err
		if !retry || ctx.Err() != nil {
			return
		}
		if attempt == 0 {
			if err := m.refreshTicket(ctx); err != nil {
				m.mu.Lock()
				s.state = "starting"
				s.message = "票据刷新失败：" + err.Error()
				m.mu.Unlock()
				return
			}
			m.mu.Lock()
			s.state = "starting"
			s.message = fmt.Sprintf("账号登录失败（error_code=%v），正在刷新账号票据后重试", s.attemptErrorCode)
			m.mu.Unlock()
		}
	}
	// Both attempts hit the runtime's ticket rejection (or the second mint
	// failed while cooling down): the final error must say so.
	m.mu.Lock()
	if s.state != "error" && !strings.Contains(s.message, "仍失败") {
		s.message += "；重试后仍失败，请稍后重试或重新扫码"
	}
	m.mu.Unlock()
}

// refreshTicket asks the login service to renew the selected account ticket.
// It is only called after a -10 rejection, never on normal launches.
func (m *Manager) refreshTicket(ctx context.Context) error {
	if m.Account == nil {
		return errors.New("缺少账号客户端")
	}
	values, err := m.Config.Read()
	if err != nil {
		return err
	}
	ref := config.String(values, "selected_account")
	if ref == "" {
		return errors.New("未选择账号")
	}
	result, err := m.Account.Request(ctx, "/accounts/refresh", "POST", map[string]any{"ref": ref})
	if err != nil {
		return err
	}
	state, ok := result.(map[string]any)
	if !ok || state["status"] != "alive" {
		return errors.New("账号刷新未确认有效，不能使用旧票据重试")
	}
	return nil
}

// attemptPaths gives every retry its own inputs/gate/worker config. Reusing
// the first attempt's directory made the worker fail its preflight, so the
// "retry" never actually started a second runtime.
type attemptPaths struct {
	dir          string
	inputs       string
	gate         string
	workerConfig string
}

func pathsForAttempt(root string, number int) attemptPaths {
	dir := root
	if number > 1 {
		dir = filepath.Join(root, fmt.Sprintf("attempt-%d", number))
	}
	return attemptPaths{
		dir:          dir,
		inputs:       filepath.Join(dir, "inputs"),
		gate:         filepath.Join(dir, "go"),
		workerConfig: filepath.Join(dir, "worker.json"),
	}
}

// attempt runs one mint/bridge/worker cycle. A runtime -10 rejection is
// reported as retryable so run() can refresh the ticket once and try again.
func (m *Manager) attempt(ctx context.Context, s *session, number int, freshTicket bool) (retry bool, runErr error) {
	paths := pathsForAttempt(s.dir, number)
	if err := os.MkdirAll(paths.dir, 0700); err != nil {
		return false, err
	}
	mintCtx, cancel := context.WithTimeout(ctx, 95*time.Second)
	mint := m.MintLaunch
	if mint == nil {
		cancel()
		return false, errors.New("缺少仅生成启动参数的启动器")
	}
	_, runErr = mint(mintCtx, s.appid, s.entry, paths.inputs)
	cancel()
	if runErr != nil {
		return false, runErr
	}
	if ctx.Err() != nil {
		return false, runErr
	}
	m.mu.Lock()
	s.inputsDir = paths.inputs
	s.manualMintStarted = false
	m.mu.Unlock()
	bridgeCtx, bridgeCancel := context.WithCancel(ctx)
	defer bridgeCancel()
	b, blog, err := m.startBridge(bridgeCtx, s)
	if err != nil {
		return false, errors.New("无法启动原生调试桥")
	}
	defer blog.Close()
	defer b.Close()
	cfg := map[string]any{"appid": s.appid, "session_id": s.id, "host": m.Host, "runtime": m.Paths.Runtime, "data_root": m.Paths.Profile, "input_dir": paths.inputs, "out": filepath.Join(s.dir, "observation"), "stop_file": filepath.Join(s.dir, "stop"), "gate_file": paths.gate, "close_appid_file": filepath.Join(os.TempDir(), "wmpf-close-bridge-"+s.id+".txt"), "seconds": s.seconds, "hide_window": s.hide, "fixture": m.fixture}
	cfgPath := paths.workerConfig
	if runErr = app.WriteJSON(cfgPath, cfg); runErr != nil {
		return false, runErr
	}
	cmd := exec.Command(m.Worker, "--config", cfgPath)
	cmd.Dir = filepath.Dir(m.Worker)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	owned, err := process.Start(cmd)
	if err != nil {
		return false, errors.New("原生观测 worker 无法启动")
	}
	defer func() {
		closeCtx, close := context.WithTimeout(context.Background(), 4*time.Second)
		defer close()
		owned.Stop(closeCtx)
	}()
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var r struct {
				Event   string         `json:"event"`
				PID     int            `json:"pid"`
				Payload map[string]any `json:"payload"`
			}
			if json.Unmarshal(scanner.Bytes(), &r) == nil {
				if r.PID > 0 {
					if r.Payload == nil {
						r.Payload = map[string]any{}
					}
					r.Payload["pid"] = r.PID
				}
				m.event(s, r.Event, r.Payload)
			}
		}
	}()
	if runErr = os.WriteFile(paths.gate, nil, 0600); runErr != nil {
		return false, runErr
	}
	timeout, stopTimeout := sessionTimeoutChannel(s.seconds)
	defer stopTimeout()
	select {
	case <-owned.Done():
		runErr = owned.Err()
	case <-ctx.Done():
		os.WriteFile(filepath.Join(s.dir, "stop"), nil, 0600)
		// Wait for the worker's explicit cleanup verdict instead of a fixed
		// 2s grace: native cleanup allows 3s and writes its verdict last.
		select {
		case <-owned.Done():
		case <-s.cleanupDone:
			select {
			case <-owned.Done():
			case <-time.After(time.Second):
			}
		case <-time.After(4 * time.Second):
		}
	case <-timeout:
		// Session duration reached: ask the worker to finish its own cleanup
		// instead of marking a normally expiring session as an error.
		os.WriteFile(filepath.Join(s.dir, "stop"), nil, 0600)
		select {
		case <-owned.Done():
		case <-s.cleanupDone:
			select {
			case <-owned.Done():
			case <-time.After(time.Second):
			}
		case <-time.After(4 * time.Second):
		}
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 4*time.Second)
	err = owned.Stop(stopCtx)
	stop()
	if err != nil {
		runErr = fmt.Errorf("原生进程清理超时: %w", err)
	}
	select {
	case <-scanned:
	case <-time.After(2 * time.Second):
	}
	m.mu.Lock()
	code := s.attemptErrorCode
	m.mu.Unlock()
	retry = ticketRejected(code)
	return retry, runErr
}

// ticketRejected is retained as a diagnostic hook. Runtime ticket rejection is
// no longer a blind refresh signal: the host now reports manual_login_required
// and the manager mints a fresh buffer only for that explicit state.
func ticketRejected(code int) bool {
	return false
}

// CloseSession's ok=true means the close flow finished. It is NOT a claim that
// native cleanup was verified; see cleanup_verified and cleanup_state.
func (m *Manager) CloseSession(ctx context.Context, id string) (any, error) {
	m.mu.Lock()
	s := m.rows[id]
	if s == nil {
		m.mu.Unlock()
		return map[string]any{"ok": false, "session_id": id, "state": "unknown", "reason": "找不到此会话"}, nil
	}
	if !terminal(s) {
		s.state = "stopping"
		s.cancel()
	}
	done := s.done
	m.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.public(s)
	out["ok"] = true
	return out, nil
}

// closeBudget must cover the worst case inside run(): waiting for the cleanup
// verdict (4s), owned.Stop (4s) and the scanner drain (2s), plus margin.
const closeBudget = 12 * time.Second

func (m *Manager) Close() {
	m.mu.Lock()
	m.closing = true
	rows := []*session{}
	for _, s := range m.rows {
		if !terminal(s) {
			s.state = "stopping"
			s.cancel()
			rows = append(rows, s)
		}
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), closeBudget)
	defer cancel()
	for _, s := range rows {
		select {
		case <-s.done:
		case <-ctx.Done():
			return
		}
	}
}

// failureReason builds the full, human-readable reason from the session state
// and the terminal error. Callers must hold m.mu (the session fields are read
// directly). It never includes cookies, tickets or CDP payloads.
func failureReason(s *session, runErr error) string {
	parts := []string{}
	if s.message != "" {
		parts = append(parts, s.message)
	}
	if runErr != nil {
		msg := runErr.Error()
		if len(parts) == 0 || !strings.Contains(parts[0], msg) {
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 {
		return "未知原因"
	}
	return strings.Join(parts, "；")
}

// recordFailure writes observation/failure.json for a terminal error session.
// Callers must hold m.mu. Failures here are best-effort and never change the
// session state — the reason is still exposed via Sessions() and Logf.
func (m *Manager) recordFailure(s *session, runErr error) {
	dir := filepath.Join(s.dir, "observation")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	row := map[string]any{
		"event": "session.failed", "session_id": s.id, "appid": s.appid,
		"state": s.state, "reason": failureReason(s, runErr),
		"login_confirmed": s.login, "launch_confirmed": s.launched,
		"debug_connected": s.debug, "worker_started": s.started,
		"cleanup_state": s.cleanupState, "failed_at": time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "failure.json"), append(data, '\n'), 0600)
}
