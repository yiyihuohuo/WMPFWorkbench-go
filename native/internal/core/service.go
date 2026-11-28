// Package core is shared by MCP and the desktop presentation layer.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"wmpfworkbench/contract"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/catalog"
	"wmpfworkbench/internal/cdp"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/dumpclient"
	"wmpfworkbench/internal/dynamic"
	"wmpfworkbench/internal/export"
	"wmpfworkbench/internal/hostruntime"
	"wmpfworkbench/internal/launch"
	"wmpfworkbench/internal/loginsvc"
	"wmpfworkbench/internal/netlog"
	"wmpfworkbench/internal/source"
	"wmpfworkbench/internal/wxapkg"
)

type Runtime interface {
	Launch(context.Context, string, string, int, *bool) (any, error)
	CloseSession(context.Context, string) (any, error)
	Sessions() []map[string]any
	Close()
}

// session IDs are only ever used as a filename fragment. Deny separators,
// drive letters and dot-only names so a caller cannot reach outside TempDir.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}$`)

func validSessionID(id string) bool {
	return sessionIDPattern.MatchString(id) && id != "." && id != ".."
}

func recoverableReadyFailure(reason string) bool {
	switch reason {
	case "host_timeout", "repeated_reset", "bad_preload", "login_failed", "watchdog", "observer_failed":
		return true
	default:
		return false
	}
}

func (s *Service) launchListing(ctx context.Context, target string) (launch.Listing, bool) {
	if s.Listing == nil {
		return launch.Listing{AppID: target, State: "listed", Listed: true}, false
	}
	row, err := s.Listing(ctx, target)
	if err != nil || row.State == "invalid" {
		return launch.Listing{AppID: target, State: "invalid"}, false
	}
	return row, row.State == "unlisted"
}

func (s *Service) activeAppID() string {
	rows := s.Sessions()
	for i := len(rows) - 1; i >= 0; i-- {
		state, _ := rows[i]["state"].(string)
		if state == "closed" || state == "error" {
			continue
		}
		if id, _ := rows[i]["appid"].(string); id != "" {
			return id
		}
	}
	return ""
}

func (s *Service) listingState(ctx context.Context, id string) (launch.Listing, bool) {
	if id == "" || s.Listing == nil {
		return launch.Listing{AppID: id, State: "listed", Listed: true}, false
	}
	row, err := s.Listing(ctx, id)
	if err != nil || row.State == "invalid" {
		return launch.Listing{AppID: id, State: "invalid"}, false
	}
	return row, row.State == "unlisted"
}

func (s *Service) sessionWebsocket(id string) string {
	for _, row := range s.Sessions() {
		if sid, _ := row["session_id"].(string); sid == id {
			ws, _ := row["cdp_websocket"].(string)
			return ws
		}
	}
	return ""
}

func (s *Service) directJump(ctx context.Context, target string) error {
	conn := s.current()
	if conn == nil || s.session == nil {
		return errors.New("CDP 未连接，无法直接跳转")
	}
	expr := fmt.Sprintf(`(() => { const b=window.WeixinJSBridge; if(!b||typeof b.invoke!=='function') return {ok:false}; b.invoke('navigateToMiniProgram',{appId:%q,envVersion:'release'},function(){}); return {ok:true}; })()`, target)
	raw, err := conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, "")
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			Value struct {
				OK bool `json:"ok"`
			} `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Result.Value.OK {
		return errors.New("直接 navigateToMiniProgram 调用失败")
	}
	return nil
}

func (s *Service) waitBridgeReady(ctx context.Context, sessionID string) (any, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, row := range s.Sessions() {
			id, _ := row["session_id"].(string)
			if id != sessionID {
				continue
			}
			setup, _ := row["setup_seen"].(bool)
			ws, _ := row["cdp_websocket"].(string)
			state, _ := row["state"].(string)
			if setup && ws != "" && state == "debug_ready" {
				return row, nil
			}
		}
		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) waitTargetActive(ctx context.Context, sessionID, target string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, row := range s.Sessions() {
			id, _ := row["session_id"].(string)
			if id != sessionID {
				continue
			}
			active, _ := row["active_appid"].(string)
			if active == target {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) rememberTwoHop(sessionID string, hop twoHopSession) {
	s.mu.Lock()
	if s.twoHop == nil {
		s.twoHop = map[string]twoHopSession{}
	}
	s.twoHop[sessionID] = hop
	s.mu.Unlock()
}

func (s *Service) markTwoHop(sessionID string, fn func(*twoHopSession)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hop, ok := s.twoHop[sessionID]
	if !ok {
		return
	}
	fn(&hop)
	s.twoHop[sessionID] = hop
}

func (s *Service) unhideSession(sessionID string) {
	if !validSessionID(sessionID) {
		return
	}
	path := filepath.Join(os.TempDir(), "wmpf-unhide-"+sessionID+".flag")
	_ = os.WriteFile(path, []byte("1"), 0600)
}

// closeBridgeSession asks the owned host to call IAppletManagerV3::CloseApplet
// for the launcher appid only. The host watches this file and never closes the
// active target appid.
func (s *Service) closeBridgeSession(sessionID, launcherAppID string) {
	if !validSessionID(sessionID) || launcherAppID == "" {
		return
	}
	path := filepath.Join(os.TempDir(), "wmpf-close-bridge-"+sessionID+".txt")
	_ = os.WriteFile(path, []byte(launcherAppID), 0600)
}

func (s *Service) scanRun(ctx context.Context, args map[string]any) (any, error) {
	root := strings.TrimSpace(str(args, "path"))
	appid, version := "", ""
	if root == "" {
		appid = s.activeAppID()
		if appid == "" {
			return map[string]any{"ok": false, "reason": "当前没有打开的小程序，无法确定扫描目标"}, nil
		}
		base := filepath.Join(s.Paths.Out, "wxapkg", appid)
		entries, err := os.ReadDir(base)
		if err == nil {
			var newest os.DirEntry
			var newestTime int64
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				if newest == nil || info.ModTime().UnixNano() > newestTime {
					newest, newestTime = entry, info.ModTime().UnixNano()
				}
			}
			if newest != nil {
				version = newest.Name()
				root = filepath.Join(base, version)
			}
		}
		if root == "" {
			task := s.Jobs.Start(wxapkg.Options{AppID: appid, Source: "runtime_profile", Timeout: 180})
			return map[string]any{"ok": false, "action": "decrypt_started", "appid": appid, "task": task,
				"message": "当前小程序尚未还原源码，已自动提交解密任务；完成后再点击扫描。"}, nil
		}
	}
	result, err := s.Dynamic.ScanRun(map[string]any{"path": root, "appid": appid, "version": version})
	if err != nil {
		return nil, err
	}
	if row, ok := result.(map[string]any); ok {
		if appid != "" {
			row["target_appid"] = appid
		}
		if version != "" {
			row["target_version"] = version
		}
		row["target_path"] = root
	}
	return result, nil
}

func annotateLaunch(row any, target string, hop twoHopSession) any {
	obj, ok := row.(map[string]any)
	if !ok {
		obj = map[string]any{}
	}
	obj["appid"] = target
	obj["requested_appid"] = target
	obj["launch_mode"] = "two_hop"
	obj["listing_state"] = hop.ListingState
	obj["bridge_hidden"] = hop.BridgeHidden
	obj["bridge_closed"] = hop.BridgeClosed
	return obj
}

type twoHopSession struct {
	RequestedAppID string
	LauncherAppID  string
	ListingState   string
	TargetActive   bool
	BridgeHidden   bool
	BridgeClosed   bool
}

type Service struct {
	Paths    app.Paths
	Config   *config.Store
	Account  *account.Client
	Local    *catalog.Local
	Online   *catalog.Online
	Jobs     *wxapkg.Manager
	LoginSvc *loginsvc.Manager
	Dump     *dumpclient.Client
	Exports  *export.Manager
	Dynamic  *dynamic.Manager
	Runtime  Runtime
	// RemoteHome and DumpWorkspace are resolved at construction and exposed for
	// login diagnostics. Credentials are never stored here.
	RemoteHome    string
	DumpWorkspace string
	// Listing classifies an appid as YingYongBao-listed or not. Tests replace it.
	Listing func(context.Context, string) (launch.Listing, error)
	// BridgeAppID is the listed appid used as an invisible jump launcher.
	BridgeAppID string
	twoHop      map[string]twoHopSession
	mu          sync.Mutex
	// lastQRIdentity caches the identity returned by the confirmed QR poll;
	// the final confirm step must not re-poll (the session is consumed).
	lastQRIdentity map[string]any
	// lastAccountCheck records when the user explicitly checked the account.
	// The sidecar's lastRefreshAt only changes on token refresh, not on a
	// manual check, so the GUI's "最近检查" column needs its own timestamp.
	lastAccountCheck time.Time
	connection       sync.Mutex
	session          *cdp.Session
}

func New(p app.Paths) *Service {
	path := os.Getenv("WMPF_CONSOLE_CONFIG")
	if path == "" {
		path = filepath.Join(p.Data, "desktop", "config.json")
	}
	cfg := config.New(path)
	cv, _ := cfg.Read()
	remoteHome := config.String(cv, "remote_home")
	if remoteHome == "" {
		remoteHome = os.Getenv("WXAPKG_REMOTE_HOME")
	}
	if remoteHome == "" {
		remoteHome = filepath.Join(p.Data, "desktop", "remote")
	}
	if os.Getenv("WXAPKG_REMOTE_HOME") == "" {
		_ = os.Setenv("WXAPKG_REMOTE_HOME", remoteHome)
	}
	_ = account.MigrateDumpRemoteHome(remoteHome)
	a := account.New(cfg)
	tool := filepath.Join(p.Root, "build", "wxapkg-offline.exe")
	if v := os.Getenv("WMPF_WXAPKG_TOOL"); v != "" {
		tool = v
	}
	// wxapkg-dump sidecar for login + arbitrary package export. Values are read
	// at construction; runtime changes (ports/paths) take effect after restart.
	dumpPort := config.Int(cv, "dump_port")
	if dumpPort < 1 || dumpPort > 65535 {
		dumpPort = 8788
	}
	workspace := config.String(cv, "dump_workspace")
	if workspace == "" {
		workspace = filepath.Join(p.Data, "desktop", "workspace")
	}
	_ = os.MkdirAll(workspace, 0700)
	dump := dumpclient.New(dumpclient.Options{
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d", dumpPort),
		ExePath:    config.String(cv, "dump_exe"),
		Workspace:  workspace,
		RemoteHome: remoteHome,
		AdminKey:   config.String(cv, "dump_admin_key"),
		AutoStart:  true,
		ManagedDir: filepath.Join(p.Data, "bin"),
	})
	launchClient := launch.New(a)
	runtimeManager := hostruntime.New(p, cfg, a)
	svc := &Service{Paths: p, Config: cfg, Account: a, Local: catalog.NewLocal(p), Online: catalog.NewOnline(a), Jobs: wxapkg.New(p, tool), LoginSvc: loginsvc.New(p, cfg), Dump: dump, Exports: export.New(dump), Runtime: runtimeManager, RemoteHome: remoteHome, DumpWorkspace: workspace, twoHop: map[string]twoHopSession{}}
	svc.Listing = launchClient.ClassifyApp
	svc.Dynamic = dynamic.New(filepath.Join(p.Data, "desktop", "dynamic"), func() dynamic.CDP {
		session := svc.current()
		if session == nil {
			return nil
		}
		return session
	})
	runtimeManager.OnSetup = func(ctx context.Context, appid string) {
		defer func() { _ = recover() }()
		if svc.Dynamic != nil {
			_, _ = svc.Dynamic.UserscriptReloadFor(ctx, appid)
			_, _ = svc.Dynamic.CloudInstall(ctx)
			_, _ = svc.Dynamic.WxapiCapture(ctx, map[string]any{"request": true, "storage": true, "bridge": true})
		}
	}
	svc.BridgeAppID = config.String(cv, "bridge_appid")
	if svc.BridgeAppID == "" {
		svc.BridgeAppID = "wx1644df8e4fe6f725"
	}
	return svc
}
func (s *Service) Close() {
	s.mu.Lock()
	if s.session != nil {
		s.session.Close()
		s.session = nil
	}
	s.mu.Unlock()
	if s.Runtime != nil {
		s.Runtime.Close()
	}
	s.Jobs.Close()
}
func (s *Service) Sessions() []map[string]any {
	rows := []map[string]any{}
	if s.Runtime != nil {
		rows = s.Runtime.Sessions()
	}
	s.mu.Lock()
	twoHop := map[string]twoHopSession{}
	for id, hop := range s.twoHop {
		twoHop[id] = hop
	}
	s.mu.Unlock()
	for _, row := range rows {
		id, _ := row["session_id"].(string)
		hop, ok := twoHop[id]
		if !ok {
			continue
		}
		display := any(hop.RequestedAppID)
		if hop.TargetActive {
			if active, ok := row["active_appid"].(string); ok && active == hop.RequestedAppID {
				display = active
			}
		}
		row["appid"] = display
		row["requested_appid"] = hop.RequestedAppID
		row["active_appid"] = display
		row["launch_mode"] = "two_hop"
		row["listing_state"] = hop.ListingState
		row["jumped"] = hop.TargetActive || row["active_appid"] != hop.LauncherAppID
		row["bridge_hidden"] = hop.BridgeHidden
		row["bridge_closed"] = hop.BridgeClosed
	}
	return rows
}
func str(args map[string]any, key string) string { v, _ := args[key].(string); return v }
func integer(args map[string]any, key string) int {
	switch n := args[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func boolean(args map[string]any, key string) bool { b, _ := args[key].(bool); return b }
func (s *Service) current() *cdp.Session           { s.mu.Lock(); defer s.mu.Unlock(); return s.session }
func notConnected() map[string]any                 { return map[string]any{"ok": false, "error": "先调 cdp_connect"} }
func (s *Service) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "search_mini_programs":
		if str(args, "source") == "online" {
			out, err := s.Online.Search(ctx, str(args, "keyword"), integer(args, "limit"))
			if err != nil {
				return map[string]any{"keyword": str(args, "keyword"), "source": "official_online_search", "grade": "blocked", "error": err.Error()}, nil
			}
			return out, nil
		}
		limit := integer(args, "limit")
		if limit < 1 || limit > 100 {
			return nil, errors.New("limit 应为 1–100")
		}
		items, err := s.Local.Search(ctx, str(args, "keyword"), limit)
		grade := "candidate"
		if len(items) > 0 {
			grade = "confirmed"
		}
		return map[string]any{"keyword": str(args, "keyword"), "hits": len(items), "items": items, "grade": grade, "source": "local_index", "note": "只覆盖本机索引，在线搜索请用 search_mini_programs"}, err
	case "index":
		return s.index(ctx, args)
	case "get_app_detail":
		id := str(args, "appid")
		if id == "" {
			id = s.activeAppID()
		}
		if id == "" {
			return nil, errors.New("appid 为空且当前没有活动会话")
		}
		listing, _ := s.listingState(ctx, id)
		indexed, err := s.Local.Get(ctx, id)
		if err != nil {
			indexed = nil
		}
		pk, err := app.ListPackages(ctx, s.Paths, id)
		if err != nil {
			return nil, err
		}
		pt, err := source.Collect(ctx, s.Paths, id)
		if err != nil {
			return nil, err
		}
		total := int64(0)
		all := len(pk) > 0
		for _, p := range pk {
			total += p.Bytes
			all = all && p.Encrypted
		}
		libs := []string{}
		for _, l := range pt.BaseLibrary {
			libs = append(libs, l.RealName)
		}
		packageSource := "local_index"
		if len(pk) > 0 {
			packageSource = pk[0].Source
		}
		return map[string]any{"appid": id, "display_appid": id, "listing_state": listing.State, "listed": listing.Listed, "launch_mode_hint": map[bool]string{true: "direct", false: "two_hop"}[listing.Listed], "package_source": packageSource, "indexed": indexed, "packages": pk, "package_bytes": total, "packages_all_encrypted": all, "plaintext_file_count": pt.PlaintextFiles, "base_library": libs}, nil
	case "extract_api_surface":
		id := str(args, "appid")
		if id == "" {
			id = s.activeAppID()
		}
		listing, _ := s.listingState(ctx, id)
		row, err := source.API(ctx, s.Paths, id, integer(args, "limit"), integer(args, "offset"))
		if row == nil {
			row = map[string]any{}
		}
		row["appid"] = id
		row["listing_state"] = listing.State
		return row, err
	case "decrypt_wxapkg":
		switch str(args, "action") {
		case "start":
			id := str(args, "appid")
			if id == "" {
				id = s.activeAppID()
			}
			return s.Jobs.Start(wxapkg.Options{AppID: id, OutDir: str(args, "out_dir"), Version: str(args, "version"), Source: str(args, "source"), Restore: boolean(args, "restore"), Pretty: boolean(args, "pretty"), Timeout: integer(args, "timeout")}), nil
		case "status":
			return s.Jobs.Status(str(args, "task_id")), nil
		case "list":
			return s.Jobs.List(10), nil
		case "cancel":
			return s.Jobs.Cancel(str(args, "task_id")), nil
		}
	case "export_mini_program":
		return s.exportPackage(ctx, args)
	case "export_status":
		return s.exportStatus(args)
	case "netlog":
		if str(args, "action") == "discover" {
			roots := []string{}
			for _, r := range s.Paths.Roots {
				roots = append(roots, r.Path)
			}
			items, err := netlog.Discover(roots)
			return map[string]any{"dirs": items}, err
		}
		if str(args, "path") == "" {
			return map[string]any{"ok": false, "grade": "blocked", "action": "analyze", "reason": "analyze 需要 path；先跑 action=discover 定位已有 NetLog"}, nil
		}
		out, err := netlog.Analyze(str(args, "path"))
		if err != nil {
			return map[string]any{"ok": false, "grade": "blocked", "action": "analyze", "path": str(args, "path"), "reason": err.Error()}, nil
		}
		return out, nil
	case "login_status":
		cfg, err := s.Config.Read()
		if err != nil {
			return nil, err
		}
		// New login backend: read the account from the local auth sidecar in
		// the same response shape the GUI already understands.
		if config.String(cfg, "login_mode") == "dump" {
			return s.dumpAccountForStatus(ctx)
		}
		var service any
		if s.LoginSvc != nil {
			service = s.LoginSvc.Status(ctx)
		}
		// login_url blank on a fresh config falls back to the login port inside
		// account.Client, so a service that is already up is reached instead of
		// reporting "登录服务尚未启动".
		accounts, err := s.Account.Request(ctx, "/accounts", "GET", nil)
		if err != nil {
			return map[string]any{"accounts": []any{}, "selected_account": "", "error": err.Error(), "login_service": service}, nil
		}
		return map[string]any{"accounts": accounts, "selected_account": config.String(cfg, "selected_account"), "login_service": service}, nil
	case "runtime_status":
		out := map[string]any{"sessions": s.Sessions(), "runtime_available": s.Paths.Describe()["exe"]}
		if boolean(args, "include_processes") {
			out["diagnostics"] = s.Paths.Describe()
			out["processes"] = []any{}
			out["diagnostics_note"] = "native process inventory not yet connected"
			if s.Runtime != nil {
				if inventory, ok := s.Runtime.(interface{ ProcessInventory() []map[string]any }); ok {
					out["processes"] = inventory.ProcessInventory()
					out["diagnostics_note"] = "process pids reported by the native observer (process.seen events); not a system-wide listing"
				}
			}
		}
		return out, nil
	case "launch":
		seconds := integer(args, "seconds")
		target := str(args, "appid")
		if !app.AppID.MatchString(target) {
			return nil, errors.New("appid 不合法")
		}
		if err := hostruntime.ValidateSessionSeconds(seconds); err != nil {
			return nil, err
		}
		if s.Runtime == nil {
			return map[string]any{"ok": false, "grade": "blocked", "reason": "原生观测 worker 尚未接入，默认启动入口仍为已验收旧版"}, nil
		}
		listing, twoHop := s.launchListing(ctx, target)
		if listing.State == "invalid" {
			return map[string]any{"ok": false, "grade": "blocked", "reason": "应用宝详情校验失败", "listing_state": "invalid"}, nil
		}
		launchAppID := target
		hop := twoHopSession{RequestedAppID: target, LauncherAppID: target, ListingState: listing.State}
		if twoHop {
			if s.BridgeAppID == "" || !app.AppID.MatchString(s.BridgeAppID) {
				return map[string]any{"ok": false, "grade": "blocked", "reason": "未配置可用的跳板小程序"}, nil
			}
			// Arm the native AppID hook before the bridge host is spawned so the
			// Frida script can read the real target from the system temp dir.
			if err := armNativeJumpHook(target); err != nil {
				return map[string]any{"ok": false, "grade": "blocked", "reason": "二跳 hook 准备失败", "error": err.Error()}, nil
			}
			launchAppID = s.BridgeAppID
			hop.LauncherAppID = s.BridgeAppID
			hop.BridgeHidden = true
		}
		var hide *bool
		if b, ok := args["hide_window"].(bool); ok {
			hide = &b
		}
		if twoHop {
			hidden := true
			hide = &hidden
		}
		launchOnce := func() (any, error) {
			return s.Runtime.Launch(ctx, launchAppID, str(args, "path"), seconds, hide)
		}
		sessionID := func(v any) string {
			if row, ok := v.(map[string]any); ok {
				id, _ := row["session_id"].(string)
				return id
			}
			return ""
		}
		result, err := launchOnce()
		if twoHop {
			s.rememberTwoHop(sessionID(result), hop)
		}
		waitReady, _ := args["wait_ready"].(bool)
		if err != nil || (!waitReady && !twoHop) {
			if twoHop {
				result = annotateLaunch(result, target, hop)
			}
			return result, err
		}
		waiter, ok := s.Runtime.(interface {
			WaitReady(context.Context, string) (any, error)
		})
		if !ok && !twoHop {
			return result, nil
		}
		var ready any
		if twoHop {
			ready, err = s.waitBridgeReady(ctx, sessionID(result))
		} else {
			ready, err = waiter.WaitReady(ctx, sessionID(result))
		}
		if err == nil {
			if row, ok := ready.(map[string]any); ok {
				row["restarted"] = false
			}
			if twoHop {
				if _, err = s.connect(ctx, s.sessionWebsocket(sessionID(result)), ""); err == nil {
					err = s.directJump(ctx, target)
				}
				if err == nil {
					err = s.waitTargetActive(ctx, sessionID(result), target)
				}
				if err == nil {
					s.markTwoHop(sessionID(result), func(h *twoHopSession) {
						h.TargetActive = true
						h.BridgeClosed = true
					})
					s.unhideSession(sessionID(result))
					s.closeBridgeSession(sessionID(result), hop.LauncherAppID)
					hop.TargetActive = true
					hop.BridgeClosed = true
				}
				ready = annotateLaunch(ready, target, hop)
			}
			return ready, err
		}
		var failure *hostruntime.ReadyFailure
		reason := ""
		if errors.As(err, &failure) {
			reason = failure.Reason
		}
		withReason := func(v any, e error) (any, error) {
			if row, ok := v.(map[string]any); ok {
				row["failure_reason"] = reason
			} else if e != nil {
				v = map[string]any{"ok": false, "failure_reason": reason, "error": e.Error()}
			}
			if twoHop {
				v = annotateLaunch(v, target, hop)
			}
			return v, e
		}
		restartBad, _ := args["restart_on_bad_preload"].(bool)
		restartAny, _ := args["restart_on_failure"].(bool)
		shouldRestart := (restartBad || restartAny) && recoverableReadyFailure(reason)
		if !shouldRestart {
			return withReason(ready, err)
		}
		_, _ = s.Runtime.CloseSession(ctx, sessionID(result))
		result, err = launchOnce()
		if err != nil {
			return withReason(result, err)
		}
		if twoHop {
			s.rememberTwoHop(sessionID(result), hop)
		}
		if twoHop {
			ready, err = s.waitBridgeReady(ctx, sessionID(result))
		} else {
			ready, err = waiter.WaitReady(ctx, sessionID(result))
		}
		if err == nil {
			if row, ok := ready.(map[string]any); ok {
				row["restarted"] = true
				row["restart_reason"] = reason
			}
			if twoHop {
				if _, err = s.connect(ctx, s.sessionWebsocket(sessionID(result)), ""); err == nil {
					err = s.directJump(ctx, target)
				}
				if err == nil {
					err = s.waitTargetActive(ctx, sessionID(result), target)
				}
				if err == nil {
					s.markTwoHop(sessionID(result), func(h *twoHopSession) {
						h.TargetActive = true
						h.BridgeClosed = true
					})
					s.unhideSession(sessionID(result))
					s.closeBridgeSession(sessionID(result), hop.LauncherAppID)
					hop.TargetActive = true
					hop.BridgeClosed = true
				}
				ready = annotateLaunch(ready, target, hop)
			}
			return ready, err
		}
		return withReason(ready, err)
	case "close_runtime":
		sid := str(args, "session_id")
		if !validSessionID(sid) {
			return map[string]any{"ok": false, "session_id": "", "state": "unknown", "reason": "session_id 无效"}, nil
		}
		if s.Runtime == nil {
			return map[string]any{"ok": false, "session_id": sid, "state": "unknown", "reason": "找不到此会话"}, nil
		}
		_ = os.Remove(filepath.Join(os.TempDir(), "wmpf-unhide-"+sid+".flag"))
		_ = os.Remove(filepath.Join(os.TempDir(), "wmpf-close-bridge-"+sid+".txt"))
		s.mu.Lock()
		delete(s.twoHop, sid)
		s.mu.Unlock()
		return s.Runtime.CloseSession(ctx, sid)
	case "cdp_local_targets":
		return s.targets(ctx, str(args, "base"))
	case "cdp_connect":
		return s.connect(ctx, str(args, "base"), str(args, "target_substr"))
	case "cdp_evaluate":
		conn := s.current()
		if conn == nil {
			return notConnected(), nil
		}
		return conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": str(args, "expression"), "returnByValue": true, "awaitPromise": false}, "")
	case "cdp_send":
		if str(args, "method") == "Page.getFrameTree" {
			return map[string]any{"ok": false, "error": "当前引擎禁用 Page.getFrameTree：已确认帧过滤器缺陷"}, nil
		}
		conn := s.current()
		if conn == nil {
			return notConnected(), nil
		}
		var params any
		if json.Unmarshal([]byte(str(args, "params_json")), &params) != nil {
			return nil, errors.New("params_json 无效")
		}
		value, err := conn.Send(ctx, str(args, "method"), params, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "result": value}, nil
	case "jump_mini_program":
		parsed, err := validateJumpArgs(args)
		if err != nil {
			return nil, err
		}
		return s.jumpMiniProgram(ctx, parsed)
	case "cdp_events":
		conn := s.current()
		if conn == nil {
			return map[string]any{"ok": false, "code": "not_connected", "error": "先调 cdp_connect"}, nil
		}
		methods := []string{}
		if list, ok := args["method_filter"].([]any); ok {
			for _, m := range list {
				methods = append(methods, m.(string))
			}
		}
		out, err := conn.Buffer.Read(ctx, cdp.ReadOptions{Methods: methods, Limit: integer(args, "limit"), Since: str(args, "since"), WaitMS: integer(args, "wait_ms"), MaxBytes: integer(args, "max_bytes")})
		if err != nil {
			code := "invalid_arguments"
			if errors.Is(err, cdp.ErrCursor) {
				code = "cursor_invalid"
			}
			return map[string]any{"ok": false, "code": code, "error": err.Error()}, nil
		}
		return out, nil
	case "cdp_scripts":
		return s.scripts(ctx, args)
	case "cloud_capture.install":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.CloudInstall(ctx)
	case "cloud_capture.list":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.CloudList(ctx)
	case "cloud_capture.replay":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.CloudReplay(ctx, args)
	case "cloud_capture.delete":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		ids := []string{}
		if raw, ok := args["ids"].([]any); ok {
			for _, item := range raw {
				if text, ok := item.(string); ok && text != "" {
					ids = append(ids, text)
				}
			}
		}
		return s.Dynamic.CloudDelete(ctx, ids)
	case "userscript.add":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.UserscriptAdd(args)
	case "userscript.list":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.UserscriptList()
	case "userscript.remove":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.UserscriptRemove(str(args, "id"))
	case "userscript.enable":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.UserscriptSetEnabled(str(args, "id"), true)
	case "userscript.disable":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.UserscriptSetEnabled(str(args, "id"), false)
	case "userscript.reload":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.UserscriptReload(ctx)
	case "wxapi.capture":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.WxapiCapture(ctx, args)
	case "wxapi.list":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.WxapiList(ctx, str(args, "category"))
	case "wxapi.replay":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.WxapiReplay(ctx, args)
	case "wxapi.delete":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		ids := []string{}
		if raw, ok := args["ids"].([]any); ok {
			for _, item := range raw {
				if text, ok := item.(string); ok && text != "" {
					ids = append(ids, text)
				}
			}
		}
		return s.Dynamic.WxapiDelete(ctx, ids)
	case "route.list":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.RouteList(ctx)
	case "route.navigate":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.RouteNavigate(ctx, args)
	case "route.guard":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.RouteGuard(ctx, args["enabled"] == true)
	case "scan.rules":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.ScanRules(args)
	case "scan.run":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.scanRun(ctx, args)
	case "scan.report":
		if s.Dynamic == nil {
			return nil, errors.New("动态分析未初始化")
		}
		return s.Dynamic.ScanReport()
	case "package.resolve":
		id := str(args, "appid")
		if id == "" {
			id = s.activeAppID()
		}
		if id == "" {
			return nil, errors.New("appid 为空且当前没有活动会话")
		}
		indexed, err := s.Local.Get(ctx, id)
		if err != nil {
			indexed = nil
		}
		packages, err := app.ListPackages(ctx, s.Paths, id)
		if err != nil {
			return nil, err
		}
		packageSource := "none"
		if len(packages) > 0 {
			packageSource = packages[0].Source
		}
		return map[string]any{"appid": id, "indexed": indexed, "packages": packages, "package_source": packageSource}, nil
	}
	return nil, errors.New("unsupported tool or action")
}
func (s *Service) index(ctx context.Context, args map[string]any) (any, error) {
	switch str(args, "action") {
	case "stats":
		return s.Local.Stats(ctx)
	case "rebuild":
		idx, err := s.Local.Rebuild(ctx)
		if err != nil {
			return nil, err
		}
		stats, err := s.Local.Stats(ctx)
		stats["app_count"] = idx.AppCount
		stats["db"] = s.Local.DBPath()
		stats["json"] = s.Local.JSONPath()
		return stats, err
	case "browse":
		limit, offset := integer(args, "limit"), integer(args, "offset")
		if limit < 1 || limit > 200 || offset < 0 {
			return nil, errors.New("invalid pagination")
		}
		idx, err := s.Local.Load(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"total": idx.AppCount, "offset": offset, "items": idx.Apps[min(offset, len(idx.Apps)):min(offset+limit, len(idx.Apps))]}, nil
	case "packages":
		idx, err := s.Local.Load(ctx)
		if err != nil {
			return nil, err
		}
		per := map[string]any{}
		count := 0
		bytes := int64(0)
		for _, a := range idx.Apps {
			rows, err := app.ListPackages(ctx, s.Paths, a.AppID)
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				continue
			}
			total := int64(0)
			files := []string{}
			all := true
			for _, r := range rows {
				total += r.Bytes
				all = all && r.Encrypted
				if !contains(files, r.File) {
					files = append(files, r.File)
				}
			}
			per[a.AppID] = map[string]any{"n": len(rows), "bytes": total, "all_encrypted": all, "files": files}
			count += len(rows)
			bytes += total
		}
		return map[string]any{"apps_with_packages": len(per), "total_packages": count, "total_bytes": bytes, "per_app": per}, nil
	}
	return nil, errors.New("unknown index action")
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if s == v {
			return true
		}
	}
	return false
}
func getJSON(ctx context.Context, base, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	resp, err := account.HTTPClient(4 * time.Second).Do(req)
	if err != nil {
		return errors.New("标准 HTTP 发现不可用")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("标准 HTTP 发现不可用")
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target)
}
func (s *Service) targets(ctx context.Context, base string) (any, error) {
	if base == "" {
		rows := []any{}
		for _, r := range s.Sessions() {
			if ws, ok := r["cdp_websocket"].(string); ok && ws != "" {
				// active_appid is the applet the host is rendering right now; it
				// differs from the launched appid after an in-app jump to a mini
				// program that is not in the app store. The websocket is the same
				// bridge port either way, so the target is debuggable.
				rows = append(rows, map[string]any{
					"id": r["session_id"], "appid": r["active_appid"],
					"launched_appid": r["appid"], "jumped": r["jumped"],
					"type": "page", "webSocketDebuggerUrl": ws,
				})
			}
		}
		return map[string]any{"targets": rows, "source": "owned_runtime"}, nil
	}
	var targets, version any
	err := getJSON(ctx, base, "/json/list", &targets)
	if err == nil {
		err = getJSON(ctx, base, "/json/version", &version)
	}
	if err != nil {
		return map[string]any{"base": base, "grade": "blocked", "error": err.Error()}, nil
	}
	return map[string]any{"base": base, "version": version, "targets": targets}, nil
}
func (s *Service) preferredCDPBase(base string) string {
	if base != "ws://127.0.0.1:62000" {
		return base
	}
	for _, row := range s.Sessions() {
		if ws, _ := row["cdp_websocket"].(string); ws != "" {
			return ws
		}
	}
	return base
}

func (s *Service) connect(ctx context.Context, base, substr string) (any, error) {
	s.connection.Lock()
	defer s.connection.Unlock()
	base = s.preferredCDPBase(base)
	u, err := url.Parse(base)
	if err != nil {
		return nil, errors.New("invalid CDP base")
	}
	var target map[string]any
	ws := base
	if u.Scheme == "http" || u.Scheme == "https" {
		var targets []map[string]any
		if err = getJSON(ctx, base, "/json/list", &targets); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		for _, t := range targets {
			if v, ok := t["webSocketDebuggerUrl"].(string); ok && v != "" && strings.Contains(fmt.Sprint(t["url"], t["title"]), substr) {
				target = t
				ws = v
				break
			}
		}
		if target == nil {
			return map[string]any{"ok": false, "error": "没有可用的 CDP 目标"}, nil
		}
	} else if u.Scheme == "ws" || u.Scheme == "wss" {
		target = map[string]any{"id": "owned-runtime", "type": "page", "webSocketDebuggerUrl": base}
		if u.Host == "127.0.0.1:62000" {
			ready := false
			for _, r := range s.Sessions() {
				if r["cdp_websocket"] != nil {
					ready = true
				}
			}
			if !ready {
				return map[string]any{"ok": false, "error": "请先独立启动并等待会话调试连接就绪"}, nil
			}
		}
	} else {
		return nil, errors.New("invalid CDP scheme")
	}
	conn, err := cdp.Dial(ctx, ws)
	if err != nil {
		return map[string]any{"ok": false, "error": "CDP 连接失败"}, nil
	}
	version, err := conn.Send(ctx, "Runtime.evaluate", map[string]any{"expression": "navigator.userAgent", "returnByValue": true}, "")
	if err != nil {
		conn.Close()
		return nil, err
	}
	s.mu.Lock()
	previous := s.session
	s.session = conn
	s.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	var response map[string]any
	json.Unmarshal(version, &response)
	result, _ := response["result"].(map[string]any)
	return map[string]any{"ok": true, "target": target, "version": result["value"]}, nil
}
func (s *Service) scripts(ctx context.Context, args map[string]any) (any, error) {
	conn := s.current()
	if conn == nil {
		return notConnected(), nil
	}
	id, uri := str(args, "script_id"), str(args, "url")
	rows := conn.Buffer.Scripts()
	if id == "" && uri == "" {
		items := []any{}
		for _, r := range rows[:min(len(rows), 300)] {
			items = append(items, map[string]any{"scriptId": r["scriptId"], "url": r["url"], "length": r["length"]})
		}
		return map[string]any{"ok": true, "count": len(rows), "index": conn.Buffer.ScriptStatus(), "items": items}, nil
	}
	offset, limit := integer(args, "offset"), integer(args, "max_bytes")
	if offset < 0 || limit < 1 || limit > 40000 {
		return nil, errors.New("offset 应 ≥ 0，max_bytes 应为 1–40000")
	}
	if id == "" {
		for _, r := range rows {
			if r["url"] == uri {
				id = fmt.Sprint(r["scriptId"])
				break
			}
		}
	}
	if id == "" {
		return nil, errors.New("找不到该 URL 的脚本")
	}
	raw, err := conn.Send(ctx, "Debugger.getScriptSource", map[string]any{"scriptId": id}, "")
	if err != nil {
		return nil, err
	}
	var response map[string]any
	json.Unmarshal(raw, &response)
	text, _ := response["scriptSource"].(string)
	chars := []rune(text)
	end := min(offset+limit, len(chars))
	start := min(offset, len(chars))
	var next any
	if end < len(chars) {
		next = end
	}
	return map[string]any{"ok": true, "bytes": len(chars), "total_bytes": len(chars), "offset": offset, "returned_bytes": end - start, "truncated": end < len(chars), "next_offset": next, "scriptSource": string(chars[start:end])}, nil
}
func (s *Service) Resource(uri string) (any, error) {
	switch uri {
	case "wmpf://status":
		return s.Paths.Describe(), nil
	case "wmpf://reference/netlog":
		return netlog.Trigger(), nil
	}
	refs, err := contract.References()
	if err != nil {
		return nil, err
	}
	if raw := refs[uri]; raw != nil {
		return raw, nil
	}
	return nil, errors.New("resource not found")
}
