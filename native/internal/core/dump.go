package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/dumpclient"
)

// exportDir resolves the configured package export directory. When unset it
// falls back to <root>/out/wxapkg-export; a relative value resolves under root.
func (s *Service) exportDir() (string, error) {
	values, err := s.Config.Read()
	if err != nil { return "", err }
	dir := config.String(values, "dump_export_dir")
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(s.Paths.Out, "wxapkg-export")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.Paths.Root, dir)
	}
	dir = filepath.Clean(dir)
	// Refuse obviously unsafe targets (root itself or the system drive root).
	if dir == s.Paths.Root || dir == filepath.VolumeName(dir)+string(os.PathSeparator) {
		return "", fmt.Errorf("导出目录不能是程序根目录或磁盘根目录")
	}
	return dir, nil
}

// exportPackage enqueues a background export task and returns a task id
// immediately. Progress is polled via export_status.
func (s *Service) exportPackage(ctx context.Context, args map[string]any) (any, error) {
	appid := strings.TrimSpace(str(args, "appid"))
	username := strings.TrimSpace(str(args, "username"))
	if appid == "" && username == "" {
		return map[string]any{"ok": false, "grade": "blocked", "reason": "appid 或 username 必填"}, nil
	}
	if appid != "" && !app.AppID.MatchString(appid) {
		return map[string]any{"ok": false, "grade": "blocked", "reason": "appid 格式应为 wx + 16 位十六进制"}, nil
	}
	dir, err := s.exportDir()
	if err != nil {
		return map[string]any{"ok": false, "grade": "blocked", "reason": err.Error()}, nil
	}
	// Caller may override the directory explicitly; still validate it is absolute.
	if override := strings.TrimSpace(str(args, "out_dir")); override != "" {
		if !filepath.IsAbs(override) {
			return map[string]any{"ok": false, "grade": "blocked", "reason": "out_dir 必须是绝对路径"}, nil
		}
		dir = filepath.Clean(override)
	}

	if err := s.Dump.Ensure(ctx); err != nil {
		return map[string]any{"ok": false, "grade": "blocked", "reason": "wxapkg-dump 服务不可用: " + err.Error(),
			"exe": s.Dump.ResolveExe(), "base": s.Dump.BaseURL()}, nil
	}
	ver := str(args, "version")
	opts := dumpclient.DownloadOptions{
		AppID: appid, Username: username, Out: dir,
		VersionType: integer(args, "version_type"), Version: ver,
		AllPackages: boolean(args, "all_packages"),
		Concurrency: integer(args, "concurrency"),
		Unpack: boolean(args, "unpack"), Beautify: boolean(args, "beautify"),
	}
	if opts.Concurrency == 0 { opts.Concurrency = 3 }
	if s.Exports == nil {
		return map[string]any{"ok": false, "grade": "blocked", "reason": "导出任务服务未初始化"}, nil
	}
	started, err := s.Exports.Start(ctx, opts)
	if err != nil {
		return map[string]any{"ok": false, "grade": "blocked", "reason": err.Error()}, nil
	}
	started["grade"] = "confirmed"
	started["appid"] = appid
	started["username"] = username
	started["out_dir"] = dir
	return started, nil
}

// exportStatus reports progress of background export tasks.
// action: status (single task or latest) | list | cancel.
func (s *Service) exportStatus(args map[string]any) (any, error) {
	if s.Exports == nil {
		return map[string]any{"ok": false, "reason": "导出任务服务未初始化"}, nil
	}
	id := str(args, "task_id")
	switch str(args, "action") {
	case "cancel":
		return s.Exports.Cancel(id), nil
	case "list":
		return map[string]any{"ok": true, "items": s.Exports.List(integer(args, "limit"))}, nil
	default:
		return s.Exports.Status(id), nil
	}
}

// DumpStatus reports the sidecar's account state and export configuration.
func (s *Service) DumpStatus(ctx context.Context) (any, error) {
	dir, _ := s.exportDir()
	out := map[string]any{"base_url": s.Dump.BaseURL(), "exe": s.Dump.ResolveExe(),
		"workspace": s.Dump.Workspace(), "export_dir": dir, "reachable": s.Dump.Reachable(ctx)}
	if err := s.Dump.Ensure(ctx); err != nil {
		out["error"] = err.Error()
		return out, nil
	}
	st, err := s.Dump.Status(ctx)
	if err != nil { out["error"] = err.Error(); return out, nil }
	out["status"] = st
	out["online"] = st.Account != nil && st.Account.Online
	return out, nil
}

// LoginEnsure brings up the local authorization service used by GUI login.
func (s *Service) LoginEnsure(ctx context.Context) error { return s.Dump.Ensure(ctx) }

// LoginStop stops a sidecar started by LoginEnsure.
func (s *Service) LoginStop() { s.Dump.Stop() }

// LoginQRCreate returns QR fields mapped to the GUI's legacy shape so the
// existing QR widget works unchanged.
func (s *Service) LoginQRCreate(ctx context.Context) (map[string]any, error) {
	if err := s.Dump.Ensure(ctx); err != nil { return nil, err }
	q, err := s.Dump.LoginQR(ctx)
	if err != nil { return nil, err }
	img := q.QRImage
	if img != "" && !strings.HasPrefix(img, "data:") { img = "data:image/png;base64," + img }
	ttl := q.ExpiresIn
	if ttl <= 0 { ttl = 240 }
	return map[string]any{"session_id": q.SessionID, "image_base64": img,
		"qr_content": q.QRContent, "expires_in": q.ExpiresIn,
		"ttl_seconds": ttl, "expires_at": time.Now().Add(time.Duration(ttl)*time.Second).UnixMilli(),
		"healthy": true}, nil
}

// LoginQRPoll polls QR state and maps it to the GUI's status shape.
// "scanned"/"authorized" both mean the phone has scanned and is awaiting
// confirmation -> reported as "authorized" which the UI renders as 已扫码待确认.
func (s *Service) LoginQRPoll(ctx context.Context, sid string) (map[string]any, error) {
	r, err := s.Dump.CheckQR(ctx, sid)
	if err != nil { return nil, err }
	status := r.Status
	if status == "scanned" { status = "authorized" }
	if status == "confirmed" && r.OpenID != "" {
		_, _ = s.Config.Update(ctx, map[string]any{"selected_account": r.OpenID})
		// Cache identity: the QR session is consumed on confirm, so the final
		// QRConfirm step must NOT call CheckQR again.
		s.mu.Lock()
		s.lastQRIdentity = map[string]any{"id": r.OpenID, "openid": r.OpenID,
			"nickname": r.NickName, "avatar": r.Avatar, "status": "confirmed"}
		s.mu.Unlock()
	}
	return map[string]any{"status": status, "state": status, "code": status,
		"nickname": r.NickName, "avatar": r.Avatar, "openid": r.OpenID, "message": r.Message}, nil
}

// LoginQRConfirm finalizes a confirmed QR login using the identity cached by
// the poll. It never re-polls: dump consumes the session on confirmation, so a
// second check returns "that login session is unknown".
func (s *Service) LoginQRConfirm(ctx context.Context, sid string) (map[string]any, error) {
	s.mu.Lock()
	cached := s.lastQRIdentity
	s.lastQRIdentity = nil
	s.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	// Fallback: read the account the sidecar has already stored.
	st, err := s.Dump.Status(ctx)
	if err != nil { return nil, err }
	openid, nick := "", ""
	if st.Account != nil { openid, nick = st.Account.OpenID, st.Account.NickName }
	if openid != "" { _, _ = s.Config.Update(ctx, map[string]any{"selected_account": openid}) }
	return map[string]any{"id": openid, "openid": openid, "nickname": nick, "status": "confirmed"}, nil
}

// dumpAccountForStatus builds a GUI-compatible account list from the sidecar's
// public account state, so login_status keeps its original response shape.
func (s *Service) dumpAccountForStatus(ctx context.Context) (any, error) {
	if s.Dump == nil {
		values, _ := s.Config.Read()
		return map[string]any{"accounts": []any{}, "selected_account": config.String(values, "selected_account"),
			"login_service": map[string]any{"running": false, "healthy": false, "error": "导出服务未初始化"}}, nil
	}
	st, err := s.Dump.Status(ctx)
	if err != nil { return nil, err }
	accounts := []map[string]any{}
	if st.Account != nil && st.Account.OpenID != "" {
		accounts = append(accounts, map[string]any{
			"id": st.Account.OpenID, "openid": st.Account.OpenID,
			"nickname": st.Account.NickName, "avatar": st.Account.Avatar,
			"status": st.Account.Status, "online": st.Account.Online,
		})
	}
	values, _ := s.Config.Read()
	return map[string]any{"accounts": accounts, "selected_account": config.String(values, "selected_account"),
		"login_service": map[string]any{"url": s.Dump.BaseURL(), "running": s.Dump.Reachable(ctx),
			"healthy": s.Dump.Reachable(ctx)}}, nil
}

