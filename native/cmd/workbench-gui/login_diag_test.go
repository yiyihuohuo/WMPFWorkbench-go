package main

import (
	"errors"
	"testing"
)

func TestLoginDiagnosticResultForUALRejection(t *testing.T) {
	out := loginDiagnosticResult(errors.New("upstream_rejected: refresh was refused (ret 0, code -109)"))
	if out["ok"] != false || out["status"] != "error" {
		t.Fatalf("bad status: %+v", out)
	}
	if out["code"] != "ual_credentials_expired" || out["relogin_required"] != true {
		t.Fatalf("bad diagnostic: %+v", out)
	}
}
