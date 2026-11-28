package dumpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
				"implementation": "native", "account": map[string]any{"online": true, "openid": "owTEST"},
			}))
		case loginQRPath:
			if r.Method != http.MethodPost {
				t.Errorf("QR method=%s", r.Method)
			}
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
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got == "" || (got != "session_expired: expired" && !contains(got, "session_expired")) {
		t.Fatalf("unexpected error: %q", got)
	}
}
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDownloadValidationAndResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != downloadPath {
			t.Errorf("path=%s", r.URL.Path)
		}
		var body DownloadOptions
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Packages) != 1 || d.Packages[0].Module != "__APP__" {
		t.Fatalf("result=%+v", d)
	}
}

func TestParseProgressRealEvents(t *testing.T) {
	cases := []struct {
		msg         string
		stage       string
		done, total int
		module      string
	}{
		{"downloading package 2/5: /app.wxapkg", "download", 2, 5, "/app.wxapkg"},
		{"reconstructing 3/12: pages/index.js", "reconstruct", 3, 12, "pages/index.js"},
		{"downloaded 5/5 package(s)", "done", 5, 5, ""},
		{"unpack finished: 42 file(s)", "reconstruct", 42, 42, ""},
	}
	for _, c := range cases {
		p := parseProgress(c.msg)
		if p.Stage != c.stage || p.Done != c.done || p.Total != c.total || p.Module != c.module {
			t.Fatalf("parse(%q) = %+v, want stage=%s done=%d total=%d module=%s",
				c.msg, p, c.stage, c.done, c.total, c.module)
		}
	}
}

func TestReachableRequiresHTTPEnvelope(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envOK("health", map[string]any{}))
	}))
	defer healthy.Close()
	if !New(Options{BaseURL: healthy.URL}).Reachable(context.Background()) {
		t.Fatal("valid envelope should be reachable")
	}

	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("foreign-service"))
	}))
	defer foreign.Close()
	if New(Options{BaseURL: foreign.URL}).Reachable(context.Background()) {
		t.Fatal("bare TCP or foreign HTTP reply must not count as reachable")
	}

	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envErr("not_ready", "starting"))
	}))
	defer failed.Close()
	if New(Options{BaseURL: failed.URL}).Reachable(context.Background()) {
		t.Fatal("envelope with ok=false must not count as reachable")
	}
}

func TestStartLockedDoesNotBindCallerContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envOK("health", map[string]any{}))
	}))
	defer srv.Close()
	oldStart := startCommand
	defer func() { startCommand = oldStart }()
	var captured context.Context
	startCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = ctx
		return exec.Command(os.Args[0], "-test.run=^$")
	}
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(Options{BaseURL: srv.URL, ExePath: "fake-sidecar"})
	if err := c.startLocked(caller); err != nil {
		t.Fatalf("startLocked: %v", err)
	}
	defer c.Stop()
	cancel()
	if captured == nil || captured == caller {
		t.Fatal("sidecar must be started with a long-lived context, not the caller's")
	}
	if captured.Err() != nil {
		t.Fatal("sidecar context must survive caller cancellation")
	}
}

func TestSubscribeEventsParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"level\":\"info\",\"message\":\"downloading package 1/3: /a.wxapkg\"}\n\n"))
		w.Write([]byte("data: {\"level\":\"info\",\"message\":\"downloaded 3/3 package(s)\"}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Progress, 4)
	go func() { _ = New(Options{BaseURL: srv.URL}).SubscribeEvents(ctx, func(p Progress) { got <- p }) }()
	p1 := <-got
	if p1.Stage != "download" || p1.Done != 1 || p1.Total != 3 {
		t.Fatalf("first event = %+v", p1)
	}
	p2 := <-got
	if p2.Stage != "done" || p2.Done != 3 {
		t.Fatalf("second event = %+v", p2)
	}
	cancel()
}
