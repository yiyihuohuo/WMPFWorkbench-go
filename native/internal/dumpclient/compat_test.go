package dumpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportsKeepAlive(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		protocol string
		allow    string
		want     bool
		wantErr  bool
	}{
		{name: "options ok", code: http.StatusOK, protocol: "1", want: true},
		{name: "cors 200 without protocol is inconclusive", code: http.StatusOK, wantErr: true},
		{name: "allow post", code: http.StatusMethodNotAllowed, allow: "POST", want: true},
		{name: "old delete only", code: http.StatusMethodNotAllowed, allow: "DELETE", want: false},
		{name: "not found", code: http.StatusNotFound, want: false},
		{name: "server error is inconclusive", code: http.StatusInternalServerError, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.allow != "" {
					w.Header().Set("Allow", tc.allow)
				}
				if tc.protocol != "" {
					w.Header().Set("x-api-protocol", tc.protocol)
				}
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()
			c := New(Options{BaseURL: srv.URL})
			got, err := c.supportsKeepAlive(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("supportsKeepAlive err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("supportsKeepAlive=%v want %v", got, tc.want)
			}
		})
	}
}

func TestSupportsKeepAliveSendsAdminKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions {
			t.Errorf("method=%s", r.Method)
		}
		if got := r.Header.Get("x-admin-key"); got != "secret" {
			t.Errorf("x-admin-key=%q", got)
		}
		w.Header().Set("x-api-protocol", "1")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, AdminKey: "secret"})
	supported, err := c.supportsKeepAlive(context.Background())
	if err != nil || !supported {
		t.Fatalf("supported=%v err=%v", supported, err)
	}
}

func TestParseListenerPIDs(t *testing.T) {
	out := `
  TCP    127.0.0.1:8788         0.0.0.0:0              LISTENING       1234
  TCP    127.0.0.1:8788         127.0.0.1:50000        ESTABLISHED     9999
  TCP    [::1]:8788             [::]:0                 LISTENING       1234
  TCP    127.0.0.1:8789         0.0.0.0:0              LISTENING       5678
`
	got := parseListenerPIDs(out, 8788)
	if len(got) != 1 || got[0] != 1234 {
		t.Fatalf("pids=%v", got)
	}
}

func TestParseListenerPIDsIgnoresForeignAndMalformedLines(t *testing.T) {
	out := `
  TCP    127.0.0.1:18788        0.0.0.0:0              LISTENING       1111
  UDP    127.0.0.1:8788         *:*                                    2222
  TCP    127.0.0.1:8788         0.0.0.0:0              ESTABLISHED     3333
  TCP    bogus                  0.0.0.0:0              LISTENING       4444
  TCP    127.0.0.1:8788         0.0.0.0:0              LISTENING       not-a-pid
  TCP    127.0.0.1:8788         0.0.0.0:0              LISTENING       5555
`
	got := parseListenerPIDs(out, 8788)
	if len(got) != 1 || got[0] != 5555 {
		t.Fatalf("pids=%v", got)
	}
}

func TestIsManagedSidecarPath(t *testing.T) {
	root := filepath.Join("C:", "workbench", "data")
	inside := filepath.Join(root, "bin", "wxapkg-dump.exe")
	outside := filepath.Join("C:", "other", "wxapkg-dump.exe")
	if !isManagedSidecarPath(inside, root) {
		t.Fatal("inside path should be managed")
	}
	if isManagedSidecarPath(outside, root) {
		t.Fatal("outside path must be rejected")
	}
	if isManagedSidecarPath(filepath.Join(root, "bin", "other.exe"), root) {
		t.Fatal("wrong image name must be rejected")
	}
	if isManagedSidecarPath(inside, "") {
		t.Fatal("empty managed dir must be rejected")
	}
}

func TestReplaceIncompatibleSidecarSafety(t *testing.T) {
	oldInspect, oldImage, oldKill := inspectListener, inspectImagePath, killPID
	defer func() { inspectListener, inspectImagePath, killPID = oldInspect, oldImage, oldKill }()

	managed := t.TempDir()
	outside := t.TempDir()
	killed := false
	killPID = func(pid int) error {
		killed = true
		return nil
	}
	c := New(Options{BaseURL: "http://127.0.0.1:1", ManagedDir: managed})

	inspectListener = func(port int) (listenerProcess, error) {
		return listenerProcess{PID: 4321, Path: filepath.Join(outside, "wxapkg-dump.exe")}, nil
	}
	if err := c.replaceIncompatibleSidecar(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "不在工作台数据目录") {
		t.Fatalf("outside process should not be killed: %v", err)
	}
	if killed {
		t.Fatal("outside process was killed")
	}

	oldPath := filepath.Join(managed, "bin", "wxapkg-dump.exe")
	inspectListener = func(port int) (listenerProcess, error) {
		return listenerProcess{PID: 4321, Path: oldPath}, nil
	}
	c.opts.ExePath = oldPath
	if err := c.replaceIncompatibleSidecar(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "内置新版未成功释放") {
		t.Fatalf("same-path old binary must not be killed: %v", err)
	}
	if killed {
		t.Fatal("same-path old binary was killed")
	}

	c.opts.ExePath = filepath.Join(managed, "bin", "versions", "new", "wxapkg-dump.exe")
	// A stale netstat snapshot must not kill a recycled PID.
	inspectImagePath = func(pid int) (string, error) {
		return filepath.Join(managed, "bin", "versions", "other", "wxapkg-dump.exe"), nil
	}
	if err := c.replaceIncompatibleSidecar(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "复核") {
		t.Fatalf("recycled PID should not be killed: %v", err)
	}
	if killed {
		t.Fatal("recycled PID was killed")
	}

	inspectImagePath = func(pid int) (string, error) { return oldPath, nil }
	if err := c.replaceIncompatibleSidecar(context.Background()); err != nil {
		t.Fatalf("managed process should be replaceable: %v", err)
	}
	if !killed {
		t.Fatal("managed old sidecar was not killed")
	}
}

func TestEnsureIncompatibleReportsOldProcess(t *testing.T) {
	oldInspect := inspectListener
	defer func() { inspectListener = oldInspect }()
	inspectListener = func(port int) (listenerProcess, error) {
		return listenerProcess{PID: 4321, Path: `C:\workbench\data\bin\wxapkg-dump.exe`}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.Header().Set("Allow", "DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprint(w, `{"ok":true,"command":"ilink status","version":"1.0.0","protocol":1,"data":{}}`)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, AutoStart: false, ManagedDir: `C:\workbench\data`})
	err := c.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "旧版 wxapkg-dump") ||
		!strings.Contains(err.Error(), "PID 4321") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureDoesNotMisreportUnknownListenerAsOldSidecar(t *testing.T) {
	oldInspect := inspectListener
	defer func() { inspectListener = oldInspect }()
	inspectListener = func(port int) (listenerProcess, error) {
		return listenerProcess{PID: 4321, Path: filepath.Join(`C:\other`, "notepad.exe")}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.Header().Set("Allow", "DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprint(w, `{"ok":true,"command":"ilink status","version":"1.0.0","protocol":1,"data":{}}`)
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, AutoStart: false})
	err := c.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "不是 wxapkg-dump") || strings.Contains(err.Error(), "旧版 wxapkg-dump") {
		t.Fatalf("unexpected error: %v", err)
	}
}
