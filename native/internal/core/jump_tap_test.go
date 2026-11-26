package core

import "testing"

func TestCdpTouchEvents(t *testing.T) {
	events := cdpTouchEvents(268, 445.9)
	if len(events) != 2 {
		t.Fatalf("expected start/end events, got %d", len(events))
	}
	if events[0]["type"] != "touchStart" || events[1]["type"] != "touchEnd" {
		t.Fatalf("unexpected event sequence: %v", events)
	}
	points, ok := events[0]["touchPoints"].([]map[string]any)
	if !ok || len(points) != 1 || points[0]["x"] != 268.0 || points[0]["y"] != 445.9 {
		t.Fatalf("unexpected touch point: %v", events[0]["touchPoints"])
	}
}
