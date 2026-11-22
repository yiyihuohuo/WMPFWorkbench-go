package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/dumpclient"
)

func dumpTestService(t *testing.T, base string) *Service {
	p, _ := app.Resolve(t.TempDir())
	cfg := config.New(t.TempDir() + "/config.json")
	_, _ = cfg.Update(context.Background(), map[string]any{"login_mode": "dump"})
	client := dumpclient.New(dumpclient.Options{BaseURL: base, AutoStart: false})
	return &Service{Paths: p, Config: cfg, Dump: client}
}

func TestLoginQRCreateMapsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "command": "ilink login qr",
			"version": "1.0.0", "protocol": 1, "data": map[string]any{
				"sessionId": "sid-1", "qrContent": "https://example/qr/sid-1",
				"qrImage": "data:image/jpeg;base64,AAAA", "expiresIn": 240}})
	}))
	defer srv.Close()
	s := dumpTestService(t, srv.URL)
	q, err := s.LoginQRCreate(context.Background())
	if err != nil { t.Fatal(err) }
	if q["session_id"] != "sid-1" || q["image_base64"] != "data:image/jpeg;base64,AAAA" {
		t.Fatalf("unmapped fields: %+v", q)
	}
	// The GUI countdown uses expires_at (absolute ms) + ttl_seconds; missing
	// either renders NaN:NaN.
	exp, ok := q["expires_at"].(int64)
	if !ok || exp <= time.Now().UnixMilli() { t.Fatalf("bad expires_at: %v", q["expires_at"]) }
	if q["ttl_seconds"] != 240 { t.Fatalf("bad ttl_seconds: %v", q["ttl_seconds"]) }
}

func TestLoginQRPollPersistsOpenID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "command": "ilink login check",
			"version": "1.0.0", "protocol": 1, "data": map[string]any{
				"status": "confirmed", "openid": "owTEST", "nickName": "tester"}})
	}))
	defer srv.Close()
	s := dumpTestService(t, srv.URL)
	p, err := s.LoginQRPoll(context.Background(), "sid-1")
	if err != nil { t.Fatal(err) }
	if p["status"] != "confirmed" { t.Fatalf("status=%v", p["status"]) }
	v, _ := s.Config.Read()
	if config.String(v, "selected_account") != "owTEST" {
		t.Fatal("confirmed openid not persisted")
	}
	// Confirm must use the cached identity: the QR session is consumed on
	// confirmation, so a second CheckQR would fail with "session unknown".
	srv.Close()
	acc, err := s.LoginQRConfirm(context.Background(), "sid-1")
	if err != nil { t.Fatalf("confirm re-polled consumed session: %v", err) }
	if acc["openid"] != "owTEST" { t.Fatalf("confirm identity=%+v", acc) }
}

func TestLoginQRPollMapsScannedToAuthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "command": "ilink login check",
			"version": "1.0.0", "protocol": 1, "data": map[string]any{"status": "scanned"}})
	}))
	defer srv.Close()
	s := dumpTestService(t, srv.URL)
	p, err := s.LoginQRPoll(context.Background(), "sid-1")
	if err != nil { t.Fatal(err) }
	if p["status"] != "authorized" {
	        t.Fatalf("scanned should map to authorized (已扫码待确认), got %v", p["status"])
	}
	}

	func TestDumpAccountForStatusMapsLastChecked(t *testing.T) {
	const last = int64(1795318401)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "command": "ilink status",
	"data": map[string]any{"implementation": "native",
	"account": map[string]any{"openid": "owTEST", "nickName": "tester",
	"status": "在线", "online": true, "lastRefreshAt": last}}})
	}))
	defer srv.Close()
	s := dumpTestService(t, srv.URL)
	out, err := s.dumpAccountForStatus(context.Background())
	if err != nil { t.Fatal(err) }
	accounts := out.(map[string]any)["accounts"].([]map[string]any)
	if len(accounts) != 1 { t.Fatalf("accounts=%v", accounts) }
	if accounts[0]["last_checked_at"] != last {
	t.Fatalf("last_checked_at not mapped: %v", accounts[0])
	}
	}
