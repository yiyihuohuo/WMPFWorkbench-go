//go:build windows

package dumpclient

import (
	"net"
	"os"
	"testing"
)

func TestQueryListenerProcessFindsCurrentTestProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	info, err := queryListenerProcess(port)
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != os.Getpid() {
		t.Fatalf("pid=%d want %d path=%s", info.PID, os.Getpid(), info.Path)
	}
	if info.Path == "" {
		t.Fatal("empty image path")
	}
}
