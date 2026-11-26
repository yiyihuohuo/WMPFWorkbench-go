package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wmpfworkbench/internal/config"
)

// Dump account file: wxapkg-dump stores the full native iLink session
// (credentials + login_buffer) in <remote-home>/account.json with schema=2.
type dumpAccountFile struct {
	Schema  int `json:"schema"`
	Account struct {
		NickName string `json:"nick_name"`
		Avatar   string `json:"avatar"`
		State    string `json:"state"`
		OpenID   string `json:"openid"`
		Device   string `json:"device"` // device_key used to mint the login buffer (wxapkg-dump uses "default")
		Session  struct {
			LoginBuffer  string `json:"login_buffer"`
			LaunchBuffer string `json:"launch_buffer"`
			ExpireTime   int64  `json:"expire_time"`
			UAL          struct {
				AccessToken  string     `json:"access_token"`
				OpenID       string     `json:"open_id"`
				RefreshToken string     `json:"refresh_token"`
				LoginType    string     `json:"login_type"`
				Scope        string     `json:"scope"`
				ExpiresIn    int64      `json:"expires_in"`
				Cookies      [][]string `json:"cookies"`
			} `json:"ual"`
		} `json:"session"`
	} `json:"account"`
}

// RemoteHome returns the active wxapkg-dump account home. Product code sets
// WXAPKG_REMOTE_HOME to a workbench-owned path before creating the client and
// starting the sidecar, so both sides read the same account.json.
func RemoteHome() string {
	if v := os.Getenv("WXAPKG_REMOTE_HOME"); v != "" {
		return v
	}
	base := os.Getenv("APPDATA")
	if base == "" {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, "wxapkg-dump", "remote")
}

func dumpRemoteHome() string { return RemoteHome() }

// MigrateDumpRemoteHome copies a legacy AppData account into a new workbench
// remote home. It never overwrites an existing target account and skips safely
// when the legacy account does not exist.
func MigrateDumpRemoteHome(target string) error {
	if target == "" {
		return nil
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(target, "account.json")); err == nil {
		return nil
	}
	base := os.Getenv("APPDATA")
	if base == "" {
		base, _ = os.UserHomeDir()
	}
	legacy, err := filepath.Abs(filepath.Join(base, "wxapkg-dump", "remote"))
	if err != nil || legacy == target {
		return nil
	}
	if _, err := os.Stat(filepath.Join(legacy, "account.json")); err != nil {
		return nil
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(legacy, "account.json"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, "account.json"), raw, 0600); err != nil {
		return err
	}
	copyDirectory(filepath.Join(legacy, "accounts"), filepath.Join(target, "accounts"))
	return nil
}

func copyDirectory(src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	_ = os.MkdirAll(dst, 0700)
	for _, entry := range entries {
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			copyDirectory(from, to)
			continue
		}
		raw, err := os.ReadFile(from)
		if err != nil {
			continue
		}
		mode := os.FileMode(0600)
		if info, err := entry.Info(); err == nil {
			mode = info.Mode().Perm()
		}
		if _, err := os.Stat(to); err == nil {
			continue
		}
		_ = os.WriteFile(to, raw, mode)
	}
}

// readDumpAccount returns the credential map and session envelope in the
// exact shape account.Client.Session() expects, sourced from the sidecar's
// account.json. No network/token is involved.
func readDumpAccount(cfg *config.Store) (map[string]any, map[string]any, error) {
	path := filepath.Join(dumpRemoteHome(), "account.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, errors.New("授权账号文件不存在，请先扫码登录")
	}
	var f dumpAccountFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, errors.New("授权账号文件损坏")
	}
	if f.Account.Session.UAL.AccessToken == "" || f.Account.OpenID == "" {
		return nil, nil, errors.New("授权账号未登录")
	}
	u := f.Account.Session.UAL
	deviceKey := strings.TrimSpace(f.Account.Device)
	if deviceKey == "" {
		deviceKey = "default" // wxapkg-dump hardcodes this when minting the buffer
	}
	creds := map[string]any{
		"openid": u.OpenID, "accesstoken": u.AccessToken, "refreshtoken": u.RefreshToken,
		"logintype": u.LoginType, "scope": strings.Trim(u.Scope, "\""), "appid": "wxd44977328b36e647",
		"nickname": f.Account.NickName, "expires_in": u.ExpiresIn,
		"expires_at": float64(time.Now().Unix() + u.ExpiresIn),
		"device_key": deviceKey,
	}
	pairs := map[string]string{}
	for _, kv := range u.Cookies {
		if len(kv) == 2 && kv[0] != "" {
			pairs[kv[0]] = kv[1]
		}
	}
	for k, v := range map[string]string{"openid": u.OpenID, "accesstoken": u.AccessToken,
		"refreshtoken": u.RefreshToken, "logintype": u.LoginType, "scope": strings.Trim(u.Scope, "\"")} {
		pairs[k] = v
	}
	chunks := []string{}
	for k, v := range pairs {
		if v != "" {
			chunks = append(chunks, k+"="+url.PathEscape(v))
		}
	}
	creds["cookie"] = strings.Join(chunks, "; ")
	creds["wxapp_openid"] = ""
	// `login_buffer` is the one-shot ticket ManualAuth already redeemed. New
	// sidecars keep a second, untouched ticket in `launch_buffer`; older files
	// do not have it, so keep the legacy fallback until the account is renewed.
	launchBuffer := strings.TrimSpace(f.Account.Session.LaunchBuffer)
	if launchBuffer == "" {
		launchBuffer = strings.TrimSpace(f.Account.Session.LoginBuffer)
	}
	env := map[string]any{"credentials": creds, "login_buffer": launchBuffer, "launch_buffer": launchBuffer,
		"account": map[string]any{"id": f.Account.OpenID, "openid": f.Account.OpenID,
			"nickname": f.Account.NickName, "avatar": f.Account.Avatar, "status": f.Account.State}}
	return env, creds, nil
}

func dumpMode(cfg *config.Store) bool {
	v, err := cfg.Read()
	return err == nil && config.String(v, "login_mode") == "dump"
}

// dumpRequest handles the legacy Request() paths from the sidecar's account
// file so callers (online search / launch) never touch the old 18766 service.
func (c *Client) dumpRequest(path, method string) (any, error) {
	env, creds, err := readDumpAccount(c.Config)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(path, "/internal/session"):
		return map[string]any{"credentials": env["credentials"], "login_buffer": env["login_buffer"], "account": env["account"]}, nil
	case path == "/accounts" && method == "GET":
		return []any{env["account"]}, nil
	case path == "/accounts/refresh":
		return map[string]any{"status": "alive"}, nil
	case strings.HasPrefix(path, "/accounts/avatar"):
		return nil, errors.New("头像请从授权服务读取")
	}
	_ = creds
	return nil, errors.New("不支持的账号操作: " + method + " " + path)
}

// dumpSession builds a Session directly from the sidecar account file.
func (c *Client) dumpSession(ctx context.Context) (Session, error) {
	env, creds, err := readDumpAccount(c.Config)
	if err != nil {
		return Session{}, err
	}
	pairs := map[string]string{}
	for _, part := range strings.Split(text(creds["cookie"]), ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			pairs[k], _ = url.PathUnescape(v)
		}
	}
	for _, k := range []string{"openid", "accesstoken", "refreshtoken", "guid", "scope", "logintype", "device_key"} {
		if v, ok := creds[k]; ok {
			pairs[k] = text(v)
		}
	}
	chunks := []string{}
	for k, v := range pairs {
		if v != "" {
			chunks = append(chunks, k+"="+Percent(v))
		}
	}
	return Session{Cookie: strings.Join(chunks, "; "), Pairs: pairs,
		SDKToken: text(creds["accesstoken"]), SDKScope: text(creds["scope"]),
		LoginBuffer: text(env["login_buffer"])}, nil
}
