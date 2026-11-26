package launch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/config"
)

func TestRealAppDetailLookup(t *testing.T) {
	id := os.Getenv("WXAPKG_APPID")
	if os.Getenv("WXAPKG_REAL_APPDETAIL") != "1" || id == "" {
		t.Skip("set WXAPKG_REAL_APPDETAIL=1 and WXAPKG_APPID=<appid>")
	}
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	_, _ = cfg.Update(context.Background(), map[string]any{"login_mode": "dump"})
	session, err := account.New(cfg).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(nil).post(context.Background(), session,
		"/pc_yyb_client/pcyyb_get_app_detail",
		map[string]any{"env_info": map[string]any{"guid": session.Pairs["guid"], "media_channel": "2100100018"}, "pkg_name": id},
		"pc_yyb", "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	t.Logf("app_detail_response=%s", raw)
}
