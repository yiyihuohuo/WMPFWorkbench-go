package dumpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func envOK(command string, data any) map[string]any {
	return map[string]any{"ok": true, "command": command, "version": "1.0.0", "protocol": 1, "data": data}
}
func envErr(code, msg string) map[string]any {
	return map[string]any{"ok": false, "command": "test", "version": "1.0.0", "protocol": 1,
		"error": map[string]any{"code": code, "message": msg}}
}

func TestStatusAndLoginQR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case statusPath:
			_ = json.NewEncoder(w).Encode(envOK("ilink status", map[string]any{
				"implementation": "native", "account": map[string]any{"online": true, "openid": "owNAX6tsSGtilmOvilnNN9iXkUfo"},
			}))
		case loginQRPath:
			if r.Method != http.MethodPost { t.Errorf("QR method=%s", r.Method) }
			_ = json.NewEncoder(w).Encode(envOK("ilink login qr", map[string]any{
				"sessionId": "s1", "qrContent": "https://open.weixin.qq.com/connect/qrcode/s1", "qrImage": "data:image/jpeg;base64,xx", "expiresIn": 240,
			}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil || st.Account == nil || !st.Account.Online {
		t.Fatalf("status: %+v err=%v", st, err)
	}
	q, err := c.LoginQR(ctx)
	if err != nil || q.SessionID != "s1" || q.ExpiresIn != 240 {
		t.Fatalf("qr: %+v err=%v", q, err)
	}
}

func TestCheckQRErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envErr("session_expired", "expired"))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	_, err := c.CheckQR(context.Background(), "s1")
	if err == nil { t.Fatal("expected error") }
	if got := err.Error(); got == "" || (got != "session_expired: expired" && !contains(got, "session_expired")) {
		t.Fatalf("unexpected error: %q", got)
	}
}
func contains(s, sub string) bool {
	if len(sub) == 0 { return true }
	for i := 0; i+len(sub) <= len(s); i++ { if s[i:i+len(sub)] == sub { return true } }
	return false
}

func TestDownloadValidationAndResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != downloadPath { t.Errorf("path=%s", r.URL.Path) }
		var body DownloadOptions
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
		if body.AppID != "wx0000000000000006" || body.Concurrency != 3 {
			t.Errorf("body=%+v", body)
		}
		_ = json.NewEncoder(w).Encode(envOK("ilink download", map[string]any{
			"appid": body.AppID, "outDir": body.Out + "/original_wxapkg/" + body.AppID + "/wxapkg",
			"packages": []map[string]any{{"module": "__APP__", "type": 4, "size": 123, "path": "wx0000000000000006.wxapkg"}},
		}))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	if _, err := c.Download(context.Background(), DownloadOptions{Out: "x"}); err == nil {
		t.Fatal("expected appid validation error")
	}
	d, err := c.Download(context.Background(), DownloadOptions{AppID: "wx0000000000000006", Out: "/tmp/out"})
	if err != nil { t.Fatal(err) }
	if len(d.Packages) != 1 || d.Packages[0].Module != "__APP__" { t.Fatalf("result=%+v", d) }
}
