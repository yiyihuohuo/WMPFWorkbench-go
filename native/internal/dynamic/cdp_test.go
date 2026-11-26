package dynamic

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCDPValue(t *testing.T) {
	raw := json.RawMessage(`{"result":{"value":{"ok":true,"items":[1,2]}}}`)
	value, err := cdpValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	obj := value.(map[string]any)
	if obj["ok"] != true {
		t.Fatalf("value=%#v", obj)
	}
}

func TestNilTypedCDPDoesNotPanic(t *testing.T) {
	var session *fakeCDP
	m := New(t.TempDir(), func() CDP { return session })
	if _, err := m.AddScript(context.Background(), "1"); err == nil {
		t.Fatal("expected not connected error")
	}
}
