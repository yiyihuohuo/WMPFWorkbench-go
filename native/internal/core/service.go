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
	"wmpfworkbench/internal/export"
	"wmpfworkbench/internal/hostruntime"
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
type Service struct {
	Paths      app.Paths
	Config     *config.Store
	Account    *account.Client
	Local      *catalog.Local
	Online     *catalog.Online
	Jobs       *wxapkg.Manager
	LoginSvc   *loginsvc.Manager
	Dump       *dumpclient.Client
	Exports    *export.Manager
	Runtime    Runtime
	mu         sync.Mutex
	// lastQRIdentity caches the identity returned by the confirmed QR poll;
	// the final confirm step must not re-poll (the session is consumed).
	lastQRIdentity map[string]any
	connection sync.Mutex
	session    *cdp.Session
}

func New(p app.Paths) *Service {
	path := os.Getenv("WMPF_CONSOLE_CONFIG")
	if path == "" {
		path = filepath.Join(p.Data, "desktop", "config.json")
	}
	cfg := config.New(path)
	a := account.New(cfg)
	tool := filepath.Join(p.Root, "build", "wxapkg-offline.exe")
	if v := os.Getenv("WMPF_WXAPKG_TOOL"); v != "" {
		tool = v
	}
	// wxapkg-dump sidecar for login + arbitrary package export. Values are read
	// at construction; runtime changes (ports/paths) take effect after restart.
	cv, _ := cfg.Read()
	dumpPort := config.Int(cv, "dump_port")
	if dumpPort < 1 || dumpPort > 65535 { dumpPort = 8788 }
	dump := dumpclient.New(dumpclient.Options{
		BaseURL:   fmt.Sprintf("http://127.0.0.1:%d", dumpPort),
		ExePath:   config.String(cv, "dump_exe"),
		Workspace: config.String(cv, "dump_workspace"),
		AdminKey:  config.String(cv, "dump_admin_key"),
		AutoStart: true,
	})
	return &Service{Paths: p, Config: cfg, Account: a, Local: catalog.NewLocal(p), Online: catalog.NewOnline(a), Jobs: wxapkg.New(p, tool), LoginSvc: loginsvc.New(p, cfg), Dump: dump, Exports: export.New(dump), Runtime: hostruntime.New(p, cfg, a)}
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
	if s.Runtime != nil {
		return s.Runtime.Sessions()
	}
	return []map[string]any{}
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
		indexed, err := s.Local.Get(ctx, id)
		if err != nil {
			return nil, err
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
		return map[string]any{"indexed": indexed, "packages": pk, "package_bytes": total, "packages_all_encrypted": all, "plaintext_file_count": pt.PlaintextFiles, "base_library": libs}, nil
	case "extract_api_surface":
		return source.API(ctx, s.Paths, str(args, "appid"), integer(args, "limit"), integer(args, "offset"))
	case "decrypt_wxapkg":
		switch str(args, "action") {
		case "start":
			return s.Jobs.Start(wxapkg.Options{AppID: str(args, "appid"), OutDir: str(args, "out_dir"), Version: str(args, "version"), Restore: boolean(args, "restore"), Pretty: boolean(args, "pretty"), Timeout: integer(args, "timeout")}), nil
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
		if !app.AppID.MatchString(str(args, "appid")) || seconds < 60 || seconds > 1800 {
			return nil, errors.New("appid 或 seconds 不合法（60–1800）")
		}
		if s.Runtime == nil {
			return map[string]any{"ok": false, "grade": "blocked", "reason": "原生观测 worker 尚未接入，默认启动入口仍为已验收旧版"}, nil
		}
		var hide *bool
		if b, ok := args["hide_window"].(bool); ok {
			hide = &b
		}
		return s.Runtime.Launch(ctx, str(args, "appid"), str(args, "path"), seconds, hide)
	case "close_runtime":
		if s.Runtime == nil {
			return map[string]any{"ok": false, "session_id": str(args, "session_id"), "state": "unknown", "reason": "找不到此会话"}, nil
		}
		return s.Runtime.CloseSession(ctx, str(args, "session_id"))
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
				rows = append(rows, map[string]any{"id": r["session_id"], "appid": r["appid"], "type": "page", "webSocketDebuggerUrl": ws})
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
func (s *Service) connect(ctx context.Context, base, substr string) (any, error) {
	s.connection.Lock()
	defer s.connection.Unlock()
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
