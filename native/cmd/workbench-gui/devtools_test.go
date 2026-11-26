package main

import "testing"

func TestCDPEndpointForQuery(t *testing.T) {
	if got := cdpEndpointForQuery("ws://127.0.0.1:62000"); got != "127.0.0.1:62000" {
		t.Fatalf("cdpEndpointForQuery=%q", got)
	}
	if got := cdpEndpointForQuery("127.0.0.1:62000"); got != "127.0.0.1:62000" {
		t.Fatalf("cdpEndpointForQuery=%q", got)
	}
}

func TestCDPEndpointFromTarget(t *testing.T) {
	cases := map[string]string{
		"ws://127.0.0.1:62000": "ws://127.0.0.1:62000",
		"devtools://devtools/bundled/inspector.html?ws=127.0.0.1:62000":   "127.0.0.1:62000",
		"devtools://devtools/bundled/inspector.html?ws=127.0.0.1%3A62000": "127.0.0.1:62000",
		"http://127.0.0.1:62000/json/list":                                "",
	}
	for in, want := range cases {
		if got := cdpEndpointFromTarget(in); got != want {
			t.Fatalf("cdpEndpointFromTarget(%q)=%q, want %q", in, got, want)
		}
	}
}
