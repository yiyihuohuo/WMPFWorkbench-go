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

// dumpStatus reports the sidecar's account state and export configuration.
func (s *Service) dumpStatus(ctx context.Context) (any, error) {
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

// loginViaDump replaces the old login service with wxapkg-dump's QR endpoints.
// action: status | qr | check | info.
func (s *Service) loginViaDump(ctx context.Context, args map[string]any) (any, error) {
	action := str(args, "action")
	if action == "" { action = "status" }
	if err := s.Dump.Ensure(ctx); err != nil {
		return map[string]any{"ok": false, "action": action, "reason": "wxapkg-dump 服务不可用: " + err.Error()}, nil
	}
	switch action {
	case "status":
		return s.dumpStatus(ctx)
	case "qr":
		q, err := s.Dump.LoginQR(ctx)
		if err != nil { return map[string]any{"ok": false, "action": action, "reason": err.Error()}, nil }
		return map[string]any{"ok": true, "action": action, "session_id": q.SessionID,
			"qr_content": q.QRContent, "qr_image": q.QRImage, "expires_in": q.ExpiresIn}, nil
	case "check":
		sid := str(args, "session_id")
		if sid == "" { sid = str(args, "session") }
		if sid == "" { return map[string]any{"ok": false, "action": action, "reason": "session_id 必填"}, nil }
		r, err := s.Dump.CheckQR(ctx, sid)
		if err != nil { return map[string]any{"ok": false, "action": action, "reason": err.Error()}, nil }
		// Persist the confirmed openid as the selected account so other tools
		// (search/export) use the just-logged-in identity.
		if r.Status == "confirmed" && r.OpenID != "" {
			_, _ = s.Config.Update(ctx, map[string]any{"selected_account": r.OpenID})
		}
		return map[string]any{"ok": true, "action": action, "status": r.Status,
			"nickname": r.NickName, "avatar": r.Avatar, "openid": r.OpenID, "message": r.Message}, nil
	}
	return nil, fmt.Errorf("invalid login action: %s", action)
}

