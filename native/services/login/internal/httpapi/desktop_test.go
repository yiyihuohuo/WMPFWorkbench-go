package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yyb_go/internal/qr"
)

func TestDesktopBridgeRequiresTokenAndReturnsCurrentAccount(t *testing.T) {
	app, acc := lifecycleApp(t, func(*http.Request) (*http.Response, error) { t.Fatal("no network expected"); return nil, nil })
	app.cfg.DesktopToken = "private-desktop-key"
	handler := app.Handler()
	for _, auth := range []string{"", "Bearer wrong"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/internal/session?ref=test-open", nil)
		r.Header.Set("Authorization", auth)
		handler.ServeHTTP(w, r)
		if w.Code != 401 || strings.Contains(w.Body.String(), "old-token") {
			t.Fatal("private bridge accepted missing/wrong authorization")
		}
	}
	creds := acc.Credentials
	creds["accesstoken"] = "latest-token"
	if err := app.db.SetAccountCredential(context.Background(), acc.ID, "saved-ticket", creds); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/internal/session?ref=test-open", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer private-desktop-key")
	handler.ServeHTTP(w, r)
	var body struct {
		Data struct {
			Credentials map[string]any `json:"credentials"`
			LoginBuffer string         `json:"login_buffer"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != 200 || body.Data.Credentials["accesstoken"] != "latest-token" || body.Data.LoginBuffer != "saved-ticket" {
		t.Fatal("bridge failed to load latest persistent credentials")
	}
}

func TestExpiredQRSessionCannotPollOrConfirm(t *testing.T) {
	app, _ := lifecycleApp(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("expired session must not reach network")
		return nil, nil
	})
	app.qrSessions["expired"] = &qr.Session{ID: "expired", CreatedAt: time.Now().Add(-10 * time.Minute)}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/qr/expired/poll", nil))
	if w.Code != http.StatusGone {
		t.Fatalf("expected expired QR response, got %d", w.Code)
	}
}

func TestQRCreationConsumesBodyBeforeNetworkFailure(t *testing.T) {
	app, _ := lifecycleApp(t, func(*http.Request) (*http.Response, error) { return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := strings.NewReader(`{}`)
	r := httptest.NewRequest("POST", "/qr?as_base64=true", body).WithContext(ctx)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	remaining, _ := io.ReadAll(r.Body)
	if len(remaining) != 0 {
		t.Fatal("QR endpoint left request body unread; large chunked replies can reset on Windows")
	}
}
