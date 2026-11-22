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
		Session  struct {
			LoginBuffer string `json:"login_buffer"`
			ExpireTime  int64  `json:"expire_time"`
			UAL         struct {
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

func dumpRemoteHome() string {
	if v := os.Getenv("WXAPKG_REMOTE_HOME"); v != "" { return v }
	base := os.Getenv("APPDATA")
	if base == "" { base, _ = os.UserHomeDir() }
	return filepath.Join(base, "wxapkg-dump", "remote")
}

// readDumpAccount returns the credential map and session envelope in the
// exact shape account.Client.Session() expects, sourced from the sidecar's
// account.json. No network/token is involved.
func readDumpAccount(cfg *config.Store) (map[string]any, map[string]any, error) {
	path := filepath.Join(dumpRemoteHome(), "account.json")
	raw, err := os.ReadFile(path)
	if err != nil { return nil, nil, errors.New("授权账号文件不存在，请先扫码登录") }
	var f dumpAccountFile
	if err := json.Unmarshal(raw, &f); err != nil { return nil, nil, errors.New("授权账号文件损坏") }
	if f.Account.Session.UAL.AccessToken == "" || f.Account.OpenID == "" {
		return nil, nil, errors.New("授权账号未登录")
	}
	u := f.Account.Session.UAL
	creds := map[string]any{
		"openid": u.OpenID, "accesstoken": u.AccessToken, "refreshtoken": u.RefreshToken,
		"logintype": u.LoginType, "scope": strings.Trim(u.Scope, "\""), "appid": "wxd44977328b36e647",
		"nickname": f.Account.NickName, "expires_in": u.ExpiresIn,
		"expires_at": float64(time.Now().Unix() + u.ExpiresIn),
	}
	pairs := map[string]string{}
	for _, kv := range u.Cookies {
		if len(kv) == 2 && kv[0] != "" { pairs[kv[0]] = kv[1] }
	}
	for k, v := range map[string]string{"openid": u.OpenID, "accesstoken": u.AccessToken,
		"refreshtoken": u.RefreshToken, "logintype": u.LoginType, "scope": strings.Trim(u.Scope, "\"")} {
		pairs[k] = v
	}
	chunks := []string{}
	for k, v := range pairs { if v != "" { chunks = append(chunks, k+"="+url.PathEscape(v)) } }
	creds["cookie"] = strings.Join(chunks, "; ")
	creds["wxapp_openid"] = ""
	env := map[string]any{"credentials": creds, "login_buffer": f.Account.Session.LoginBuffer,
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
	if err != nil { return nil, err }
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
	if err != nil { return Session{}, err }
	pairs := map[string]string{}
	for _, part := range strings.Split(text(creds["cookie"]), ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok { pairs[k], _ = url.PathUnescape(v) }
	}
	for _, k := range []string{"openid", "accesstoken", "refreshtoken", "guid", "scope", "logintype"} {
		if v, ok := creds[k]; ok { pairs[k] = text(v) }
	}
	chunks := []string{}
	for k, v := range pairs { if v != "" { chunks = append(chunks, k+"="+Percent(v)) } }
	return Session{Cookie: strings.Join(chunks, "; "), Pairs: pairs,
		SDKToken: text(creds["accesstoken"]), SDKScope: text(creds["scope"]),
		LoginBuffer: text(env["login_buffer"])}, nil
}
