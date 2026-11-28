package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyLoginErrorUALRejected(t *testing.T) {
	err := errors.New("upstream_rejected: keepalive refresh failed (the refresh was refused (ret 0, code -109)); check login failed")
	diag := ClassifyLoginError(err)
	if diag.Code != "ual_credentials_expired" || !diag.ReloginRequired {
		t.Fatalf("diag=%+v", diag)
	}
	if diag.Detail == err.Error() || diag.Detail != "ual_credentials_expired" {
		t.Fatalf("detail must be a fixed summary, got %q", diag.Detail)
	}
}

func TestClassifyLoginErrorQRExpired(t *testing.T) {
	diag := ClassifyLoginError(errors.New("that login session is unknown"))
	if diag.Code != "qr_session_expired" || diag.ReloginRequired {
		t.Fatalf("diag=%+v", diag)
	}
}

func TestClassifyLoginErrorSidecarUnavailable(t *testing.T) {
	diag := ClassifyLoginError(errors.New("dial tcp 127.0.0.1:8788: connect: connection refused"))
	if diag.Code != "sidecar_unavailable" {
		t.Fatalf("diag=%+v", diag)
	}
}

func TestClassifyLoginErrorOldSidecar(t *testing.T) {
	diag := ClassifyLoginError(errors.New("检测到旧版 wxapkg-dump 正在占用端口 8788（PID 1234）；该版本不支持账号保活接口"))
	if diag.Code != "sidecar_outdated" || diag.ReloginRequired {
		t.Fatalf("diag=%+v", diag)
	}
}

func TestClassifyLoginErrorProbeInconclusive(t *testing.T) {
	cases := []string{
		"OPTIONS /api/ilink/account/keepalive 返回 HTTP 405 Method Not Allowed",
		"无法确认 wxapkg-dump 版本（账号保活探测失败）：缺少 x-api-protocol",
	}
	for _, text := range cases {
		diag := ClassifyLoginError(errors.New(text))
		if diag.Code != "sidecar_probe_inconclusive" || diag.ReloginRequired {
			t.Fatalf("diag for %q = %+v", text, diag)
		}
		if diag.Detail == text || strings.Contains(diag.Detail, "405 Method") {
			t.Fatalf("raw error text leaked into detail: %q", diag.Detail)
		}
	}
}

func TestLoginDiagnosticMapDoesNotLeakRawError(t *testing.T) {
	raw := "探测账号保活接口返回 HTTP 405 Method Not Allowed; token=SECRET123; path=C:\\Users\\private\\data"
	m := LoginDiagnosticMap(errors.New(raw))
	blob := fmt.Sprint(m)
	if strings.Contains(blob, "SECRET123") || strings.Contains(blob, `C:\Users`) || strings.Contains(blob, raw) {
		t.Fatalf("raw error leaked: %s", blob)
	}
}

func TestValidSessionIDRejectsPathTraversal(t *testing.T) {
	for _, id := range []string{"s1", "session-12_ab", "ab.1@x"} {
		if !validSessionID(id) {
			t.Fatalf("valid session id rejected: %q", id)
		}
	}
	for _, id := range []string{"", ".", "..", "../evil", `..\evil`, `C:\temp\x`, "/tmp/x", "a/b", strings.Repeat("a", 129)} {
		if validSessionID(id) {
			t.Fatalf("unsafe session id accepted: %q", id)
		}
	}
}
