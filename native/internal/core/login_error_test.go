package core

import (
	"errors"
	"testing"
)

func TestClassifyLoginErrorUALRejected(t *testing.T) {
	err := errors.New("upstream_rejected: keepalive refresh failed (the refresh was refused (ret 0, code -109)); check login failed")
	diag := ClassifyLoginError(err)
	if diag.Code != "ual_credentials_expired" || !diag.ReloginRequired {
		t.Fatalf("diag=%+v", diag)
	}
	if diag.Detail != err.Error() {
		t.Fatalf("detail missing: %+v", diag)
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
