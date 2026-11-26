package account

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectDumpAccountNoSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WXAPKG_REMOTE_HOME", home)
	raw := `{"schema":2,"account":{"nick_name":"tester","openid":"owTEST","state":"online","device":"default","session":{"login_buffer":"LOGIN","launch_buffer":"LAUNCH","expire_time":1,"ual":{"access_token":"SECRET_TOKEN","refresh_token":"SECRET_REFRESH","expires_in":0}}}}`
	if err := os.WriteFile(filepath.Join(home, "account.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	info := InspectDumpAccount()
	if !info.Exists || info.Schema != 2 || !info.HasAccessToken || !info.HasRefreshToken || !info.HasLoginBuffer || !info.HasLaunchBuffer {
		t.Fatalf("info=%+v", info)
	}
	if info.ExpiresIn != 0 || !info.ExpiresAtMissing {
		t.Fatalf("expiry info=%+v", info)
	}
	joined := ""
	for _, part := range []string{info.NickName, info.OpenID, info.State, info.AccountFile, info.RemoteHome} {
		joined += part
	}
	if strings.Contains(joined, "SECRET_TOKEN") || strings.Contains(joined, "SECRET_REFRESH") || strings.Contains(joined, "LOGIN") || strings.Contains(joined, "LAUNCH") {
		t.Fatalf("secret leaked in diagnostic: %+v", info)
	}
}
