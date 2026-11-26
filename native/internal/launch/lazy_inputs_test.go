package launch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wmpfworkbench/internal/account"
)

func TestMintLaunchDefersLoginBuffer(t *testing.T) {
	loginBufferCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.Contains(r.URL.Path, "get_app_detail"):
			_, _ = io.WriteString(w, `{"code":0,"data":{"apps":{"one":{"pkg_name":"`+testID+`","status":1,"game_type":2}}}}`)
		case strings.Contains(r.URL.Path, "encrypt"):
			_, _ = io.WriteString(w, `{"code":0,"encrypt_param":{"iv":"IV","data":"DATA","timestamp":"123","authtag":"TAG"}}`)
		case strings.Contains(r.URL.Path, "login_buffer"):
			loginBufferCalls++
			_, _ = io.WriteString(w, `{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := New(nil)
	client.Endpoint = srv.URL
	client.Session = func(context.Context) (account.Session, error) {
		return account.Session{Pairs: map[string]string{"guid": "guid", "accesstoken": "main"}, SDKToken: "sdk", SDKScope: "snsapi_runtime_pcsdk", LoginBuffer: "dGlja2V0"}, nil
	}
	dest := filepath.Join(t.TempDir(), "inputs")
	meta, err := client.MintLaunch(context.Background(), testID, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	if loginBufferCalls != 0 {
		t.Fatalf("MintLaunch called login_buffer endpoint %d times", loginBufferCalls)
	}
	if _, err := os.Stat(filepath.Join(dest, "login.txt")); !os.IsNotExist(err) {
		t.Fatalf("login.txt should not exist after MintLaunch, err=%v", err)
	}
	for _, name := range []string{"launch.bin", "game_type.txt", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if meta["provider"] != "launch_only" {
		t.Fatalf("provider=%v", meta["provider"])
	}
	if err := client.MintLoginBuffer(context.Background(), dest, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "login.txt"))
	if err != nil || string(data) != "dGlja2V0" {
		t.Fatalf("login.txt=%q err=%v", string(data), err)
	}
}
