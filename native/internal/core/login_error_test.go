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
