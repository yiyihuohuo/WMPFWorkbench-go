package account

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DumpAccountInfo is a non-sensitive summary of the sidecar account file.
// It never contains tokens, cookies, or the full login buffer.
type DumpAccountInfo struct {
	RemoteHome       string `json:"remote_home"`
	AccountFile      string `json:"account_file"`
	Exists           bool   `json:"exists"`
	Schema           int    `json:"schema"`
	State            string `json:"state"`
	OpenID           string `json:"openid"`
	NickName         string `json:"nickname"`
	HasAccessToken   bool   `json:"has_access_token"`
	HasRefreshToken  bool   `json:"has_refresh_token"`
	HasLoginBuffer   bool   `json:"has_login_buffer"`
	HasLaunchBuffer  bool   `json:"has_launch_buffer"`
	ExpiresIn        int64  `json:"expires_in"`
	ExpiresAtMissing bool   `json:"expires_at_missing"`
	ParseError       string `json:"parse_error,omitempty"`
}

func InspectDumpAccount() DumpAccountInfo {
	home := RemoteHome()
	out := DumpAccountInfo{RemoteHome: home, AccountFile: filepath.Join(home, "account.json"), ExpiresAtMissing: true}
	raw, err := os.ReadFile(out.AccountFile)
	if err != nil {
		return out
	}
	out.Exists = true
	var f dumpAccountFile
	if err := json.Unmarshal(raw, &f); err != nil {
		out.ParseError = err.Error()
		return out
	}
	out.Schema = f.Schema
	out.State = f.Account.State
	out.OpenID = f.Account.OpenID
	out.NickName = f.Account.NickName
	u := f.Account.Session.UAL
	out.HasAccessToken = u.AccessToken != ""
	out.HasRefreshToken = u.RefreshToken != ""
	out.HasLoginBuffer = f.Account.Session.LoginBuffer != ""
	out.HasLaunchBuffer = f.Account.Session.LaunchBuffer != ""
	out.ExpiresIn = u.ExpiresIn
	return out
}
