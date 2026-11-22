package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// exportPackage implements the export_mini_program tool. It delegates the
// actual download/unpack to the wxapkg-dump sidecar via its HTTP API.
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
	res, err := s.Dump.Download(ctx, opts)
	if err != nil {
		return map[string]any{"ok": false, "grade": "blocked", "reason": err.Error(),
			"appid": appid, "username": username, "out_dir": dir}, nil
	}
	return map[string]any{"ok": true, "grade": "confirmed", "source": "wxapkg-dump",
		"appid": res.AppID, "username": res.Username, "out_dir": res.OutDir,
		"unpacked_dir": res.UnpackedDir, "packages": res.Packages}, nil
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
	return map[string]any{"session_id": q.SessionID, "image_base64": img,
		"qr_content": q.QRContent, "expires_in": q.ExpiresIn, "healthy": true}, nil
}

// LoginQRPoll polls QR state and maps it to the GUI's legacy status shape.
func (s *Service) LoginQRPoll(ctx context.Context, sid string) (map[string]any, error) {
	r, err := s.Dump.CheckQR(ctx, sid)
	if err != nil { return nil, err }
	if r.Status == "confirmed" && r.OpenID != "" {
		_, _ = s.Config.Update(ctx, map[string]any{"selected_account": r.OpenID})
	}
	status := r.Status
	if status == "scanned" || status == "authorized" { status = "scanned" }
	return map[string]any{"status": status, "state": status, "code": status,
		"nickname": r.NickName, "avatar": r.Avatar, "openid": r.OpenID, "message": r.Message}, nil
}

// LoginQRConfirm is retained for the GUI's final confirm step; the identity is
// already persisted by the poll when the state turns confirmed.
func (s *Service) LoginQRConfirm(ctx context.Context, sid string) (map[string]any, error) {
	r, err := s.Dump.CheckQR(ctx, sid)
	if err != nil { return nil, err }
	openid := r.OpenID
	if openid == "" {
		if st, e := s.Dump.Status(ctx); e == nil && st.Account != nil { openid = st.Account.OpenID }
	}
	if openid != "" { _, _ = s.Config.Update(ctx, map[string]any{"selected_account": openid}) }
	return map[string]any{"id": openid, "openid": openid, "nickname": r.NickName, "status": r.Status}, nil
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

