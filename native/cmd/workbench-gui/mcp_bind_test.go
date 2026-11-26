package main

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// nonLoopbackIPv4 returns a local LAN address that is NOT 127.0.0.1, used to
// prove that mcp_bind actually changes the listening interface.
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skip("cannot enumerate interfaces")
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			ip := ipnet.IP.To4()
			if ip != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	t.Skip("no non-loopback IPv4 address available")
	return ""
}

func TestMCPBindControlsListeningInterface(t *testing.T) {
	lan := nonLoopbackIPv4(t)
	port := 19123

	// loopback only: LAN address must refuse.
	l1, err := mcpListen("127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(lan, strconv.Itoa(port)), 500*time.Millisecond); err == nil {
		c.Close()
		l1.Close()
		t.Fatal("mcp_bind=127.0.0.1 must not accept LAN connections")
	}
	l1.Close()

	// wildcard: LAN address must connect.
	l2, err := mcpListen("0.0.0.0", port)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(lan, strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("mcp_bind=0.0.0.0 must accept LAN connections: %v", err)
	}
	c.Close()
}
