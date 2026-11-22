package launch

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wmpfworkbench/internal/account"
)

const testID = "wx0123456789abcdef"

func TestConfigMatchesFrozenPythonBytes(t *testing.T) {
	cfg, f27, err := BuildConfig(testID, map[string]any{"iv": "IV", "data": "DATA", "timestamp": "123", "authtag": "TAG"})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("0a12777830313233343536373839616263646566421c7b22726571756972655f68776e645f6e6f74696679223a747275657dda01280a12777864343439373733323862333665363437120249561a044441544122033132332a03544147")
	if !bytes.Equal(cfg, want) || len(f27) != 40 {
		t.Fatalf("cfg=%x f27=%d", cfg, len(f27))
	}
}
func TestMintSignsActualBodyAndUsesSDKToken(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `\u0026`) {
			t.Error("HTML escaping changed body")
		}
		key, salt := "", ""
		if strings.Contains(r.URL.Path, "encrypt") {
			key = "84e752c6ed95c057"
			salt = "df683a7f1c814b4f"
		}
		if strings.Contains(r.URL.Path, "login_buffer") {
			key = "wgrdg373hy26ww2"
			if !bytes.Contains(raw, []byte("sdk-token")) || bytes.Contains(raw, []byte("main-token")) {
				t.Error("wrong token namespace")
			}
		}
		if key != "" {
			sig := md5.Sum([]byte(string(raw) + r.Header.Get("Ual-Access-Timestamp") + salt + key + r.Header.Get("Ual-Access-Nonce")))
			if r.Header.Get("Ual-Access-Signature") != hex.EncodeToString(sig[:]) {
				t.Error("signature mismatch")
			}
		}
		switch r.URL.Path {
		case "/pc_yyb_client/pcyyb_get_app_detail":
			io.WriteString(w, `{"code":0,"data":{"apps":{"one":{"pkg_name":"wx0123456789abcdef","status":1,"game_type":2,"app_name":"测试"}}}}`)
		case "/v3/pcyyb_encrypt_wx_mini_program_launch_param":
			io.WriteString(w, `{"code":0,"encrypt_param":{"iv":"IV","data":"DATA","timestamp":"123","authtag":"TAG"}}`)
		default:
			io.WriteString(w, `{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`)
		}
	}))
	defer srv.Close()
	client := New(nil)
	client.Endpoint = srv.URL
	session := account.Session{Cookie: "fixture=1", Pairs: map[string]string{"guid": "guid", "accesstoken": "main-token"}, SDKToken: "sdk-token", SDKScope: "snsapi_runtime_pcsdk"}
	client.Session = func(context.Context) (account.Session, error) { return session, nil }
	dest := filepath.Join(t.TempDir(), "inputs")
	meta, err := client.Mint(context.Background(), testID, "?q=中文&x=1", dest)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || meta["game_type"] != 2 {
		t.Fatalf("mint %+v requests %d", meta, requests)
	}
	raw, _ := os.ReadFile(filepath.Join(dest, "login.txt"))
	if string(raw) != "dGlja2V0" {
		t.Fatalf("ticket not published")
	}
	raw, _ = json.Marshal(meta)
	if bytes.Contains(raw, []byte("dGlja2V0")) || bytes.Contains(raw, []byte("sdk-token")) {
		t.Fatal("metadata leaked credentials")
	}
}

func TestMintFreshFetchesTicketInsteadOfUsingCachedBuffer(t *testing.T) {
	var ticketFetches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pc_yyb_client/pcyyb_get_app_detail":
			io.WriteString(w, `{"code":0,"data":{"apps":{"one":{"pkg_name":"wx0123456789abcdef","status":1,"game_type":2}}}}`)
		case "/v3/pcyyb_encrypt_wx_mini_program_launch_param":
			io.WriteString(w, `{"code":0,"encrypt_param":{"iv":"IV","data":"DATA","timestamp":"123","authtag":"TAG"}}`)
		case "/pc_yyb_auth/pcyyb_get_wx_login_buffer_auth":
			ticketFetches++
			body, _ := io.ReadAll(r.Body)
			if !bytes.Contains(body, []byte(`"mydevice"`)) {
				t.Errorf("user_id must be device_key, not guid; body=%s", body)
			}
			io.WriteString(w, `{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["bmV3LXRpY2tldA=="]}}}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := New(nil)
	client.Endpoint = srv.URL
	client.Session = func(context.Context) (account.Session, error) {
		return account.Session{Pairs: map[string]string{"guid": "guid", "device_key": "mydevice"}, SDKToken: "sdk-token", SDKScope: "snsapi_runtime_pcsdk", LoginBuffer: "b2xkLXRpY2tldA=="}, nil
	}
	dest := filepath.Join(t.TempDir(), "inputs")
	oldMeta, err := client.Mint(context.Background(), testID, "", filepath.Join(t.TempDir(), "old-inputs"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := client.MintFresh(context.Background(), testID, "", dest)
	if err != nil {
		t.Fatal(err)
	}
	if ticketFetches != 1 {
		t.Fatalf("fresh ticket endpoint called %d times, want exactly once", ticketFetches)
	}
	raw, err := os.ReadFile(filepath.Join(dest, "login.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "bmV3LXRpY2tldA==" {
		t.Fatalf("used ticket %q, want freshly fetched ticket", raw)
	}
	if meta["login_sha256"] == "" {
		t.Fatal("fresh ticket fingerprint missing")
	}
	if meta["login_sha256"] == oldMeta["login_sha256"] {
		t.Fatal("retry metadata still identifies the cached ticket")
	}
}

func TestMintRejectsInvalidTicketWithoutPublishing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "detail") {
			io.WriteString(w, `{"data":{"apps":{"one":{"pkg_name":"wx0123456789abcdef","status":1,"game_type":3}}}}`)
		} else {
			io.WriteString(w, `{"encrypt_param":{"iv":"IV","data":"DATA","timestamp":"123","authtag":"TAG"}}`)
		}
	}))
	defer srv.Close()
	client := New(nil)
	client.Endpoint = srv.URL
	client.Session = func(context.Context) (account.Session, error) {
		return account.Session{Pairs: map[string]string{"guid": "guid"}, SDKScope: "snsapi_runtime_pcsdk", LoginBuffer: "invalid!"}, nil
	}
	dest := filepath.Join(t.TempDir(), "inputs")
	_, err := client.Mint(context.Background(), testID, "", dest)
	if err == nil {
		t.Fatal("invalid ticket accepted")
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
	        t.Fatalf("published directory before validation: %v", err)
	}
	}

	func TestPostRetriesTransportFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	calls++
	if calls == 1 {
	// Simulate a dropped connection (transport error), not a 5xx.
	hj, ok := w.(http.Hijacker)
	if ok {
	conn, _, _ := hj.Hijack()
	conn.Close()
	return
	}
	w.WriteHeader(http.StatusInternalServerError)
	return
	}
	body, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(body), "pkg_name") {
	t.Errorf("retry lost request body: %s", body)
	}
	io.WriteString(w, `{"code":0,"data":{"apps":{}}}`)
	}))
	defer srv.Close()
	client := New(nil)
	client.Endpoint = srv.URL
	session := account.Session{Pairs: map[string]string{"guid": "g"}}
	res, err := client.post(context.Background(), session, "/pc_yyb_client/pcyyb_get_app_detail",
	map[string]any{"pkg_name": testID}, "pc_yyb", "", "")
	if err != nil { t.Fatal(err) }
	if res == nil || calls != 2 {
	t.Fatalf("expected 2 attempts (transport fail then success), got calls=%d res=%v", calls, res)
	}
	}
