// Command wmpf-gui is the Go desktop presentation layer. Its pages mirror the
// original PySide6 console (overview / accounts / tools / search / logs / about)
// and share core.Service with MCP. Credentials stay in Go: the frontend only
// receives masked account data and session state.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"wmpfworkbench/contract"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/bundle"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/core"
	"wmpfworkbench/internal/hostruntime"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/app_icon.jpg
var iconData []byte

const appVersion = "1.0.5"

type Backend struct {
	ctx         context.Context
	svc         *core.Service
	cfgPath     string
	mu          sync.Mutex
	logs        []string
	mcpError    string
	mcpCancel   context.CancelFunc
	mcpDone     chan struct{}
	skipMCP     bool // tests set this to avoid launching a real listener
	qrLastState string
	loginState  string // last account-state signature; avoids log spam every 5s

	devtoolsFrontend *exec.Cmd
	devtoolsPort     string
	devtoolsProfile  string
	devtoolsLog      string
}

func (b *Backend) Startup(ctx context.Context) {
	b.ctx = ctx
	// Stream account-client diagnostics into the GUI log panel.
	b.svc.Account.Logf = b.logf
	// The HTTP MCP endpoint in the clipboard config must actually listen, or
	// clients report "fetch failed"; the GUI therefore owns its lifecycle.
	if values, err := b.svc.Config.Read(); err == nil && values["mcp_enabled"] != false {
		b.startMCP()
	}
	go func() {
		if err := b.svc.LoginEnsure(ctx); err != nil {
			b.logErr("授权服务自动启动", err, "")
			return
		}
		b.logf("授权服务已自动启动（%s）", b.svc.Dump.BaseURL())
	}()
}

// MCPStatus reports whether something is listening on the configured MCP HTTP
// port, so the UI shows real state instead of assuming availability.
func (b *Backend) MCPStatus() map[string]any {
	values, err := b.svc.Config.Read()
	port := 0
	enabled := true
	bind := "127.0.0.1"
	if err == nil {
		port = config.Int(values, "mcp_port")
		enabled = values["mcp_enabled"] != false
		if v := config.String(values, "mcp_bind"); v != "" {
			bind = v
		}
	}
	if port < 1 || port > 65535 {
		port = 18767
	}
	conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	running := dialErr == nil
	if conn != nil {
		conn.Close()
	}
	b.mu.Lock()
	mcpError := b.mcpError
	b.mu.Unlock()
	out := map[string]any{"running": running, "enabled": enabled, "port": port, "bind": bind, "url": fmt.Sprintf("http://127.0.0.1:%d/mcp", port)}
	if !running && mcpError != "" {
		out["error"] = mcpError
	}
	return out
}

func (b *Backend) setMCPError(text string) {
	b.mu.Lock()
	b.mcpError = text
	b.mu.Unlock()
}

// startMCP launches the in-process HTTP MCP server with a cancelable context.
func (b *Backend) startMCP() {
	if b.skipMCP {
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	done := make(chan struct{})
	b.mu.Lock()
	b.mcpCancel = cancel
	b.mcpDone = done
	b.mu.Unlock()
	go func() {
		defer close(done)
		if err := serveMCP(ctx, b.svc, "http", 0); err != nil {
			if ctx.Err() == nil {
				b.setMCPError(err.Error())
				b.logf("MCP HTTP 服务未启动：%v", err)
			}
			return
		}
		if ctx.Err() == nil {
			b.setMCPError("")
			b.logf("MCP HTTP 服务已退出")
		}
	}()
}

// stopMCP cancels the running listener and waits for it to release the port.
func (b *Backend) stopMCP() {
	b.mu.Lock()
	cancel, done := b.mcpCancel, b.mcpDone
	b.mcpCancel, b.mcpDone = nil, nil
	b.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			b.logf("MCP HTTP 服务停止超时")
		}
	}
}

var (
	openidPattern = regexp.MustCompile(`ow[A-Za-z0-9_-]{10,}`)
	bearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]+`)
)

// maskLog keeps account identifiers and tokens out of the visible operation log.
func maskLog(text string) string {
	text = openidPattern.ReplaceAllStringFunc(text, func(match string) string { return match[:6] + "…" })
	return bearerPattern.ReplaceAllString(text, "Bearer ***")
}

func (b *Backend) logf(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logs = append(b.logs, time.Now().Format("15:04:05")+"   "+maskLog(fmt.Sprintf(format, args...)))
	if len(b.logs) > 400 {
		b.logs = b.logs[len(b.logs)-400:]
	}
}

// logErr records a failure with the full original error text and optional
// context. Callers should use this instead of dropping err.Error() so users
// can see exactly what went wrong in the GUI log panel.
func (b *Backend) logErr(operation string, err error, context string) {
	if err == nil {
		return
	}
	line := operation + "失败：" + err.Error()
	if context != "" {
		line += "（" + context + "）"
	}
	b.logf("%s", line)
}

type ToolView struct {
	Name        string          `json:"name"`
	Group       string          `json:"group"`
	Description string          `json:"description"`
	Enabled     bool            `json:"enabled"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

func toolGroup(name string) string {
	switch {
	case strings.HasPrefix(name, "search_"), strings.HasPrefix(name, "get_app"), name == "index":
		return "搜索与索引"
	case name == "login_status":
		return "账号"
	case name == "launch", name == "close_runtime", name == "runtime_status":
		return "运行时"
	case strings.HasPrefix(name, "cdp_"):
		return "调试"
	case name == "decrypt_wxapkg", name == "extract_api_surface":
		return "源码"
	case name == "netlog":
		return "网络"
	case strings.HasPrefix(name, "cloud_"), strings.HasPrefix(name, "userscript."), strings.HasPrefix(name, "wxapi."), strings.HasPrefix(name, "route."), strings.HasPrefix(name, "scan."), strings.HasPrefix(name, "package."):
		return "动态分析"
	}
	return "其他"
}

func (b *Backend) Bootstrap() map[string]any {
	return map[string]any{
		"root":     b.svc.Paths.Root,
		"version":  appVersion,
		"sessions": b.svc.Sessions(),
		"tools":    b.Tools(),
		"config":   b.Config(),
		"logs":     b.Logs(),
	}
}

func (b *Backend) Config() map[string]any {
	values, err := b.svc.Config.Read()
	if err != nil {
		return map[string]any{}
	}
	hide, _ := values["hide_miniapp_window"].(bool)
	enabled, _ := values["mcp_enabled"].(bool)
	login, _ := values["login_port"].(float64)
	mcpPort, _ := values["mcp_port"].(float64)
	selected, _ := values["selected_account"].(string)
	bind, _ := values["mcp_bind"].(string)
	if bind == "" {
		bind = "127.0.0.1"
	}
	return map[string]any{
		"hide_miniapp_window": hide,
		"mcp_enabled":         enabled,
		"login_port":          int(login),
		"mcp_port":            int(mcpPort),
		"mcp_bind":            bind,
		"selected_account":    selected,
	}
}

func (b *Backend) SavePorts(loginPort, mcpPort int, bind string) (map[string]any, error) {
	if loginPort < 1024 || loginPort > 65535 || mcpPort < 1024 || mcpPort > 65535 {
		return nil, fmt.Errorf("端口范围应为 1024–65535")
	}
	if bind == "" {
		bind = "127.0.0.1"
	}
	if bind != "127.0.0.1" && bind != "0.0.0.0" {
		return nil, fmt.Errorf("MCP 监听地址只能是 127.0.0.1 或 0.0.0.0")
	}
	// The authorization sidecar owns its own port (dump_port); login_port is
	// kept only for config compatibility and no longer starts a local service.
	if _, err := b.svc.Config.Update(b.ctx, map[string]any{"login_port": float64(loginPort), "mcp_port": float64(mcpPort), "mcp_bind": bind}); err != nil {
		return nil, err
	}
	b.logf("设置已保存：MCP %d（监听 %s）；授权服务端口由运行配置决定", mcpPort, bind)
	// Apply the new MCP port/bind immediately instead of requiring a GUI restart.
	values, _ := b.svc.Config.Read()
	if values["mcp_enabled"] != false {
		b.stopMCP()
		b.startMCP()
		b.logf("MCP HTTP 服务已按新设置重启")
	}
	return b.Config(), nil
}

// DumpStatus reports the wxapkg-dump sidecar state and the effective export
// directory. It is used by the search page's export card.
func (b *Backend) DumpStatus() (any, error) {
	out, err := b.svc.DumpStatus(b.ctx)
	if err != nil {
		b.logErr("读取导出服务状态", err, "")
		return out, err
	}
	return out, nil
}

// SaveExportDir persists the user-configured package export directory. An
// empty value clears the override and falls back to the default.
func (b *Backend) SaveExportDir(dir string) (any, error) {
	dir = strings.TrimSpace(dir)
	if _, err := b.svc.Config.Update(b.ctx, map[string]any{"dump_export_dir": dir}); err != nil {
		return nil, err
	}
	if dir == "" {
		b.logf("导出目录已恢复默认")
	} else {
		b.logf("导出目录已保存：%s", dir)
	}
	return b.DumpStatus()
}

// PickExportDir opens a native folder picker so the export directory can be
// configured without typing a path.
func (b *Backend) PickExportDir() (string, error) {
	path, err := runtime.OpenDirectoryDialog(b.ctx, runtime.OpenDialogOptions{Title: "选择小程序包导出目录"})
	if err != nil || path == "" {
		return "", err
	}
	return path, nil
}

// ExportPackage exports an arbitrary mini program's packages via the
// wxapkg-dump sidecar. Args mirror the export_mini_program MCP tool.
func (b *Backend) ExportPackage(appid, username, outDir string, versionType int, version string, allPackages, unpack, beautify bool) (any, error) {
	ctxInfo := "appid=" + appid
	if appid == "" {
		ctxInfo = "username=" + username
	}
	b.logf("导出小程序包：%s", ctxInfo)
	args := map[string]any{
		"appid": appid, "username": username, "out_dir": outDir,
		"version_type": versionType, "version": version,
		"all_packages": allPackages, "unpack": unpack, "beautify": beautify,
		"concurrency": 3,
	}
	result, err := b.svc.Call(b.ctx, "export_mini_program", args)
	if err != nil {
		b.logErr("导出小程序包", err, ctxInfo)
		return nil, err
	}
	if row, ok := result.(map[string]any); ok {
		if row["ok"] == true {
			b.logf("导出任务已提交：%s → %v", ctxInfo, row["out_dir"])
		} else {
			b.logf("导出提交失败：%v", row["reason"])
		}
	}
	return result, nil
}

// ExportStatus polls a background export task (status/list/cancel).
func (b *Backend) ExportStatus(action, taskID string, limit int) (any, error) {
	return b.svc.Call(b.ctx, "export_status", map[string]any{
		"action": action, "task_id": taskID, "limit": limit,
	})
}

// LoginStatus returns masked account information plus service state. Failures
// are logged in full so "logged in but no accounts" is diagnosable from the GUI.
func (b *Backend) LoginStatus() (any, error) {
	out, err := b.svc.Call(b.ctx, "login_status", nil)
	if err != nil {
		b.logErr("读取登录状态", err, "")
		return out, err
	}
	if row, ok := out.(map[string]any); ok {
		var accounts []any
		switch value := row["accounts"].(type) {
		case []any:
			accounts = value
		case []map[string]any:
			for _, item := range value {
				accounts = append(accounts, item)
			}
		}
		ids := make([]string, 0, len(accounts))
		for _, item := range accounts {
			if acc, ok := item.(map[string]any); ok {
				nick, _ := acc["nickname"].(string)
				openid, _ := acc["openid"].(string)
				if nick != "" {
					ids = append(ids, nick)
				} else if openid != "" {
					ids = append(ids, openid)
				}
			}
		}
		// One signature per state; the UI refreshes every 5s, so logging on
		// every poll would flood the panel with identical lines.
		state := strings.Join(ids, ",")
		if diag, ok := row["diagnostic"].(map[string]any); ok && diag != nil {
			code, _ := diag["code"].(string)
			if code == "" {
				code = "login_error"
			}
			state = "diagnostic:" + code
		} else if msg, ok := row["error"].(string); ok && msg != "" {
			state = "error:" + msg
		}
		b.mu.Lock()
		changed := b.loginState != state
		b.loginState = state
		b.mu.Unlock()
		if changed {
			switch {
			case strings.HasPrefix(state, "diagnostic:"):
				if diag, ok := row["diagnostic"].(map[string]any); ok {
					message, _ := diag["message"].(string)
					if message == "" {
						message = "请重新扫码登录"
					}
					b.logf("登录状态异常：%s", message)
				}
			case len(accounts) == 0:
				b.logf("账号列表为空：请确认已扫码登录，或查看上方登录状态异常原因")
			default:
				b.logf("账号列表已加载：共 %d 个（%s）", len(accounts), strings.Join(ids, ", "))
			}
		}
	}
	return out, nil
}

func (b *Backend) StartLogin() (any, error) {
	if err := b.svc.LoginEnsure(b.ctx); err != nil {
		b.logErr("登录服务启动", err, "")
		return map[string]any{"healthy": false, "error": err.Error()}, err
	}
	b.logf("授权服务已就绪（%s）", b.svc.Dump.BaseURL())
	return map[string]any{"healthy": true, "url": b.svc.Dump.BaseURL()}, nil
}

func (b *Backend) StopLogin() any {
	b.svc.LoginStop()
	b.logf("授权服务已停止")
	return map[string]any{"healthy": false, "stopping": true}
}

func loginDiagnosticResult(err error) map[string]any {
	out := core.LoginDiagnosticMap(err)
	out["ok"] = false
	out["status"] = "error"
	return out
}

func (b *Backend) RefreshAccount() (any, error) {
	// The GUI refreshes the account every few seconds, including on a fresh
	// install where the sidecar has never been started. Ensure it is up before
	// reading status, otherwise the first refresh logs a connection-refused
	// error even though the bundled binary is present and usable.
	if b.svc.Dump != nil {
		if err := b.svc.Dump.Ensure(b.ctx); err != nil {
			b.logErr("账号检查", err, "授权服务启动失败")
			return loginDiagnosticResult(err), nil
		}
	}
	// "Check account" must renew credentials, not just read the cached status.
	// The sidecar performs check_login -> optional refresh -> login_buffer ->
	// ManualAuth and persists the rotated session before we report success.
	if _, err := b.svc.Dump.KeepAlive(b.ctx); err != nil {
		diag := core.ClassifyLoginError(err)
		b.logErr("账号保活", err, diag.Message)
		return loginDiagnosticResult(err), nil
	}
	st, err := b.svc.Dump.Status(b.ctx)
	if err != nil {
		b.logErr("账号检查", err, "请确认授权服务可用")
		return loginDiagnosticResult(err), nil
	}
	if st.Account == nil {
		return map[string]any{"ok": false, "status": "no_account", "reason": "请先扫码登录", "relogin_required": true}, nil
	}
	checkedAt := b.svc.MarkAccountChecked()
	b.logf("账号检查完成：%s（保活已执行）", st.Account.Status)
	return map[string]any{"ok": st.Account.Online, "status": st.Account.Status,
		"openid": st.Account.OpenID, "nickname": st.Account.NickName,
		"checked_at": checkedAt}, nil
}

func (b *Backend) SelectAccount(ref string) error {
	// Switch in the sidecar (multi-account) so subsequent launches use the new
	// account's session, then keep the local config mirror in sync.
	if b.svc.Dump != nil {
		if err := b.svc.Dump.SelectAccount(b.ctx, ref); err != nil {
			b.logErr("切换当前账号", err, "openid="+ref)
			return err
		}
	}
	if _, err := b.svc.Config.Update(b.ctx, map[string]any{"selected_account": ref}); err != nil {
		return err
	}
	b.logf("已切换当前账号")
	return nil
}

// DeleteAccount removes one remembered account from the sidecar.
func (b *Backend) DeleteAccount(openid string) (any, error) {
	if b.svc.Dump == nil {
		return map[string]any{"ok": false, "reason": "授权服务未初始化"}, nil
	}
	if err := b.svc.Dump.DeleteAccount(b.ctx, openid); err != nil {
		b.logErr("删除账号", err, "openid="+openid)
		return nil, err
	}
	b.logf("已删除账号")
	return map[string]any{"ok": true, "openid": openid}, nil
}

// Avatar returns the account's profile image URL from the authorization
// sidecar. The frontend puts it directly in an <img src>.
func (b *Backend) Avatar(ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil
	}
	st, err := b.svc.Dump.Status(b.ctx)
	if err != nil || st.Account == nil {
		return "", err
	}
	return st.Account.Avatar, nil
}

func (b *Backend) QRCreate() (any, error) {
	b.mu.Lock()
	b.qrLastState = ""
	b.mu.Unlock()
	b.logf("正在生成登录二维码…")
	result, err := b.svc.LoginQRCreate(b.ctx)
	if err != nil {
		b.logErr("二维码生成", err, "请确认授权服务已启动且网络可用")
		return nil, err
	}
	b.logf("新二维码已生成，等待微信扫描")
	return result, nil
}

func (b *Backend) QRPoll(session string) (any, error) {
	value, err := b.svc.LoginQRPoll(b.ctx, session)
	if err != nil {
		// Polling is high-frequency; only surface a failure once per state.
		b.mu.Lock()
		changed := b.qrLastState != "poll_error"
		b.qrLastState = "poll_error"
		b.mu.Unlock()
		if changed {
			b.logErr("扫码状态轮询", err, "若持续失败，请重新生成二维码")
		}
		return nil, err
	}
	state := ""
	for _, key := range []string{"status", "state", "code", "message"} {
		if v, ok := value[key].(string); ok && v != "" {
			state = key + "=" + v
			break
		}
	}
	if state != "" {
		b.mu.Lock()
		changed := b.qrLastState != state
		b.qrLastState = state
		b.mu.Unlock()
		if changed {
			b.logf("扫码状态：%s", state)
		}
	}
	return value, nil
}

func (b *Backend) QRConfirm(session string) (any, error) {
	value, err := b.svc.LoginQRConfirm(b.ctx, session)
	if err != nil {
		b.logErr("扫码登录确认", err, "二维码可能已过期，请重新生成")
		return nil, err
	}
	b.logf("扫码登录完成，账号已保存并设为当前账号")
	return value, nil
}

func (b *Backend) Search(keyword, source string) (any, error) {
	if strings.TrimSpace(keyword) == "" {
		return map[string]any{"items": []any{}, "hits": 0}, nil
	}
	if source != "local" {
		source = "online"
	}
	b.logf("搜索：%s（%s）", keyword, source)
	result, err := b.svc.Call(b.ctx, "search_mini_programs", map[string]any{"keyword": keyword, "source": source, "limit": 30})
	if err != nil {
		b.logErr("搜索小程序", err, "关键词="+keyword)
		return nil, err
	}
	return result, nil
}

func (b *Backend) Launch(appid, entry string, seconds int, hide bool) (any, error) {
	if _, err := b.svc.Config.Update(b.ctx, map[string]any{"hide_miniapp_window": hide}); err != nil {
		b.logErr("保存窗口设置", err, "")
		return nil, err
	}
	duration := "不限时"
	if seconds > 0 {
		duration = fmt.Sprintf("%ds", seconds)
	}
	ctxInfo := fmt.Sprintf("appid=%s, 时长=%s, 隐藏窗口=%v", appid, duration, hide)
	if entry != "" {
		ctxInfo += ", 入口=" + entry
	}
	b.logf("打开并调试：%s", ctxInfo)
	launchCtx, cancel := context.WithTimeout(b.ctx, 120*time.Second)
	defer cancel()
	result, err := b.svc.Call(launchCtx, "launch", map[string]any{
		"appid": appid, "path": entry, "seconds": float64(seconds),
		"hide_window": hide, "wait_ready": true, "restart_on_failure": true,
	})
	if err != nil {
		// preflight failures (e.g. missing runtime DLL) arrive here with a full,
		// actionable message from ValidateRuntime; show it verbatim.
		b.logErr("启动小程序", err, ctxInfo)
		return nil, err
	}
	if row, ok := result.(map[string]any); ok {
		if fmt.Sprint(row["state"]) == "error" {
			detail := fmt.Sprint(row["error"])
			if reason := fmt.Sprint(row["reason"]); reason != "" && reason != "<nil>" {
				detail += "；原因: " + reason
			}
			b.logf("会话启动失败：%s（%s）", detail, ctxInfo)
		} else {
			b.logf("会话已创建：%v（%s）", row["state"], ctxInfo)
		}
	}
	return result, nil
}

func (b *Backend) Close(session string) (any, error) {
	result, err := b.svc.Call(b.ctx, "close_runtime", map[string]any{"session_id": session})
	if err == nil {
		b.logf("已关闭会话 %s", session)
	}
	return result, err
}

func (b *Backend) Sessions() []map[string]any { return b.svc.Sessions() }

func (b *Backend) Tools() []ToolView {
	tools, _, err := contract.Catalog()
	if err != nil {
		return []ToolView{}
	}
	out := make([]ToolView, 0, len(tools))
	for _, tool := range tools {
		out = append(out, ToolView{Name: tool.Name, Group: toolGroup(tool.Name), Description: tool.Description, Enabled: b.svc.Config.Enabled(tool.Name), Schema: tool.InputSchema})
	}
	return out
}

func (b *Backend) SetTool(name string, enabled bool) ([]ToolView, error) {
	return b.BulkTools([]string{name}, enabled)
}

func (b *Backend) BulkTools(names []string, enabled bool) ([]ToolView, error) {
	values, err := b.svc.Config.Read()
	if err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	for _, name := range names {
		targets[name] = true
	}
	disabled := []any{}
	if list, ok := values["disabled_tools"].([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok && !targets[text] {
				disabled = append(disabled, text)
			}
		}
	}
	if !enabled {
		for name := range targets {
			disabled = append(disabled, name)
		}
	}
	if _, err = b.svc.Config.Update(b.ctx, map[string]any{"disabled_tools": disabled}); err != nil {
		return nil, err
	}
	b.logf("已%s %d 个工具", map[bool]string{true: "启用", false: "禁用"}[enabled], len(names))
	return b.Tools(), nil
}

func (b *Backend) GlobalTools(enabled bool) (map[string]any, error) {
	if _, err := b.svc.Config.Update(b.ctx, map[string]any{"mcp_enabled": enabled}); err != nil {
		return nil, err
	}
	b.logf("MCP 总开关：%v", enabled)
	return b.Config(), nil
}

// Icon returns the original project icon for the About page, so the UI does
// not carry a hand-drawn placeholder.
func (b *Backend) Icon() string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(iconData)
}

// OpenURL opens an external documentation link in the system browser.
func (b *Backend) OpenURL(target string) {
	if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") {
		runtime.BrowserOpenURL(b.ctx, target)
	}
}

// OpenReport opens only the generated scan report, never an arbitrary file URL.
func (b *Backend) OpenReport() (string, error) {
	report := filepath.Join(b.svc.Paths.Data, "desktop", "dynamic", "scan-report.html")
	st, err := os.Stat(report)
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("扫描报告尚未生成")
	}
	runtime.BrowserOpenURL(b.ctx, "file:///"+filepath.ToSlash(report))
	return report, nil
}

func devtoolsBrowserCandidates() []string {
	out := []string{}
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	localAppData := os.Getenv("LOCALAPPDATA")
	appendPath := func(root, rel string) {
		if root == "" {
			return
		}
		out = append(out, filepath.Join(root, rel))
	}
	appendPath(programFiles, `Google\Chrome\Application\chrome.exe`)
	appendPath(programFilesX86, `Google\Chrome\Application\chrome.exe`)
	appendPath(localAppData, `Google\Chrome\Application\chrome.exe`)
	appendPath(programFiles, `Microsoft\Edge\Application\msedge.exe`)
	appendPath(programFilesX86, `Microsoft\Edge\Application\msedge.exe`)
	out = append(out, `C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`)
	return out
}

func findDevtoolsBrowser() string {
	for _, candidate := range devtoolsBrowserCandidates() {
		if candidate != "" {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	for _, name := range []string{"chrome.exe", "msedge.exe", "google-chrome.exe", "microsoft-edge.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func cdpEndpointForQuery(endpoint string) string {
	if i := strings.Index(endpoint, "://"); i >= 0 {
		return endpoint[i+3:]
	}
	return endpoint
}

func cdpEndpointFromTarget(target string) string {
	if strings.HasPrefix(target, "ws://") || strings.HasPrefix(target, "wss://") {
		return target
	}
	if i := strings.Index(target, "ws="); i >= 0 {
		endpoint := target[i+3:]
		if j := strings.IndexAny(endpoint, "&#"); j >= 0 {
			endpoint = endpoint[:j]
		}
		if decoded, err := url.QueryUnescape(endpoint); err == nil {
			endpoint = decoded
		}
		return endpoint
	}
	return ""
}

func readDevtoolsPort(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", fmt.Errorf("DevToolsActivePort 为空")
	}
	return strings.TrimSpace(lines[0]), nil
}

func verifyDevtoolsFrontend(port string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/json/version")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DevTools 前端返回 HTTP %d", resp.StatusCode)
	}
	return nil
}

func (b *Backend) stopDevtoolsFrontendLocked() {
	if b.devtoolsFrontend != nil && b.devtoolsFrontend.Process != nil && b.devtoolsFrontend.ProcessState == nil {
		pid := strconv.Itoa(b.devtoolsFrontend.Process.Pid)
		_ = exec.Command("taskkill", "/PID", pid, "/T", "/F").Run()
	}
	if b.devtoolsProfile != "" {
		_ = os.RemoveAll(b.devtoolsProfile)
	}
	b.devtoolsFrontend, b.devtoolsPort, b.devtoolsProfile, b.devtoolsLog = nil, "", "", ""
}

// devtoolsFrontendURL starts a short-lived headless Chromium with an isolated
// profile and returns its bundled DevTools frontend URL. Only the exact process
// started here is ever stopped; Chrome/Edge is never killed by process name.
func (b *Backend) devtoolsFrontendURL(endpoint string) (string, error) {
	queryEndpoint := cdpEndpointForQuery(endpoint)
	frontendURL := func(port string) string {
		return "http://127.0.0.1:" + port + "/devtools/inspector.html?ws=" + queryEndpoint
	}
	b.mu.Lock()
	cmd, port, profile := b.devtoolsFrontend, b.devtoolsPort, b.devtoolsProfile
	b.mu.Unlock()
	if cmd != nil && cmd.ProcessState == nil && port != "" && profile != "" && verifyDevtoolsFrontend(port) == nil {
		return frontendURL(port), nil
	}

	b.mu.Lock()
	b.stopDevtoolsFrontendLocked()
	b.mu.Unlock()

	browser := findDevtoolsBrowser()
	if browser == "" {
		return "", fmt.Errorf("未找到 Chrome 或 Edge，无法启动 DevTools 前端")
	}
	profile, err := os.MkdirTemp("", "wmpf-devtools-frontend-*")
	if err != nil {
		return "", err
	}
	logPath := profile + ".log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		_ = os.RemoveAll(profile)
		return "", err
	}
	portFile := filepath.Join(profile, "DevToolsActivePort")
	cmd = exec.Command(browser, "--headless=new", "--disable-gpu", "--no-first-run",
		"--no-default-browser-check", "--user-data-dir="+profile,
		"--remote-debugging-port=0", "--remote-allow-origins=*", "about:blank")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = os.RemoveAll(profile)
		return "", err
	}
	go func() { _ = cmd.Wait(); _ = logFile.Close() }()

	deadline := time.Now().Add(20 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil {
			break
		}
		port, err = readDevtoolsPort(portFile)
		if err == nil && port != "" && verifyDevtoolsFrontend(port) == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		b.mu.Lock()
		b.stopDevtoolsFrontendLocked()
		b.mu.Unlock()
		return "", fmt.Errorf("DevTools 前端服务启动失败，日志：%s", logPath)
	}
	b.mu.Lock()
	b.devtoolsFrontend, b.devtoolsPort, b.devtoolsProfile, b.devtoolsLog = cmd, port, profile, logPath
	b.mu.Unlock()
	return frontendURL(port), nil
}

// OpenDebugger always uses the system default browser. A headless Chromium may
// be started only as a local static server for its bundled DevTools frontend;
// it is never used as the user-visible browser. If that frontend server cannot
// start, the default browser opens the CDP discovery page instead.
func (b *Backend) OpenDebugger(target string) error {
	endpoint := cdpEndpointFromTarget(target)
	if endpoint == "" {
		runtime.BrowserOpenURL(b.ctx, target)
		return nil
	}
	frontend, err := b.devtoolsFrontendURL(endpoint)
	if err != nil {
		b.logf("DevTools 前端服务未启动：%v；已改为打开 CDP 目标列表。可查看错误中的前端日志定位原因。", err)
		runtime.BrowserOpenURL(b.ctx, "http://"+endpoint+"/json/list")
		return nil
	}
	runtime.BrowserOpenURL(b.ctx, frontend)
	return nil
}

// mcpConfig mirrors the original console's stdio/http client entries: stdio
// carries type/command/args/env/description, http carries type/url/headers.
func mcpConfig(kind, exe, root string, values map[string]any) (map[string]any, error) {
	switch kind {
	case "stdio":
		return map[string]any{
			"type":    "stdio",
			"command": exe,
			"args":    []string{"-mcp", "stdio", "-root", root},
			"env": map[string]string{
				"WMPF_HOME":      root,
				"WMPF_AUTH_MODE": "managed",
			},
			"description": "WMPF Workbench（stdio，与 GUI 共享账号与开关）",
		}, nil
	case "http":
		return map[string]any{
			"type":        "streamable-http",
			"url":         fmt.Sprintf("http://127.0.0.1:%d/mcp", config.Int(values, "mcp_port")),
			"headers":     map[string]string{"Authorization": "Bearer " + config.String(values, "mcp_token")},
			"description": "WMPF Workbench（HTTP，与 GUI 共用运行会话）",
		}, nil
	default:
		return nil, fmt.Errorf("未知配置类型")
	}
}

// Invoke calls a service/MCP tool from the GUI. The GUI uses one generic
// binding to avoid growing the already large Backend method list.
func (b *Backend) Invoke(tool, argsJSON string) (any, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return nil, fmt.Errorf("tool 不能为空")
	}
	args := map[string]any{}
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return nil, err
		}
	}
	return b.svc.Call(b.ctx, tool, args)
}

// CopyMCPConfig puts a ready-to-paste MCP client configuration on the
// clipboard, matching the original console's stdio/http entries.
func (b *Backend) CopyMCPConfig(kind string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	values, err := b.svc.Config.Read()
	if err != nil {
		return err
	}
	entry, err := mcpConfig(kind, exe, b.svc.Paths.Root, values)
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(map[string]any{"mcpServers": map[string]any{"wmpf": entry}}); err != nil {
		return err
	}
	runtime.ClipboardSetText(b.ctx, buffer.String())
	b.logf("已复制 %s MCP 配置", kind)
	return nil
}

func (b *Backend) Logs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.logs))
	copy(out, b.logs)
	return out
}

func (b *Backend) ClearLogs() []string {
	b.mu.Lock()
	b.logs = nil
	b.mu.Unlock()
	return []string{}
}

func main() {
	root := flag.String("root", os.Getenv("WMPF_HOME"), "installation root")
	runtimeDir := flag.String("runtime", "", "override runtime directory (default <root>/runtime/5.10.7300.414)")
	cfgPath := flag.String("config", "", "override console configuration path")
	selfCheck := flag.Bool("selfcheck", false, "create the window, then exit")
	mcpMode := flag.String("mcp", "", "run as MCP server: stdio or http")
	mcpPort := flag.Int("mcp-port", 0, "MCP HTTP port (default from config)")
	transport := flag.String("transport", "", "MCP transport alias: stdio or streamable-http")
	port := flag.Int("port", 0, "MCP HTTP port alias")
	flag.Parse()
	if *runtimeDir != "" {
		os.Setenv("WMPF_RUNTIME_DIR", *runtimeDir)
	}
	mode := *mcpMode
	if mode == "" && *transport != "" {
		switch *transport {
		case "stdio":
			mode = "stdio"
		case "streamable-http":
			mode = "http"
		}
	}
	if mode != "" {
		chosen := *mcpPort
		if chosen == 0 {
			chosen = *port
		}
		if err := runMCP(*root, mode, chosen, *cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	p, err := app.Resolve(*root)
	if err != nil {
		os.Exit(1)
	}
	if helpers, err := bundle.Ensure(p.Data); err == nil {
		bundle.Apply(helpers)
	}
	svc := core.New(p)
	if *cfgPath != "" {
		svc.Config = config.New(*cfgPath)
		svc.Account.Config = svc.Config
		svc.Runtime = hostruntime.New(p, svc.Config, svc.Account)
	}
	defer svc.Close()
	backend := &Backend{svc: svc, cfgPath: *cfgPath}
	// Route asynchronous runtime failures (e.g. early mint/ticket errors that
	// never reach the observer's events.jsonl) into the GUI log panel.
	if rt, ok := svc.Runtime.(*hostruntime.Manager); ok {
		rt.Logf = backend.logf
	}
	err = wails.Run(&options.App{
		Title:     "小程序工作台",
		Width:     920,
		Height:    620,
		MinWidth:  560,
		MinHeight: 420,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: backend.Startup,
		Bind:      []any{backend},
		OnDomReady: func(ctx context.Context) {
			exe, _ := os.Executable()
			go func() {
				time.Sleep(200 * time.Millisecond)
				applyWindowIcon("小程序工作台", exe)
			}()
			if *selfCheck {
				go func() {
					time.Sleep(700 * time.Millisecond)
					runtime.Quit(ctx)
				}()
			}
		},
	})
	if err != nil {
		os.Exit(1)
	}
}
