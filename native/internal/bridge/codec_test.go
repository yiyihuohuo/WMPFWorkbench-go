package bridge

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

func TestNodeFramesAndGoRoundTrip(t *testing.T) {
	raw, err := os.ReadFile("testdata/node-frames.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct{ Frame, Category, Payload string }
	if json.Unmarshal(raw, &fixtures) != nil {
		t.Fatal("fixture")
	}
	for _, f := range fixtures {
		data, _ := base64.StdEncoding.DecodeString(f.Frame)
		frame, err := Decode(data)
		if err != nil || frame.Category != f.Category || frame.Payload != f.Payload {
			t.Fatal(frame, err)
		}
		encoded, err := Encode(5, "chromeDevtools", f.Payload)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(encoded)
		if err != nil || decoded.Payload != f.Payload {
			t.Fatal(decoded, err)
		}
	}
	for _, raw := range [][]byte{{255}, {10, 255}, {0}} {
		if _, err := Decode(raw); err == nil {
			t.Fatalf("accepted malformed %x", raw)
		}
	}
}

// ConnectJsContext is a separate protobuf message (field 1 = jscontextId), not
// the chromeDevtools wrapper. Encoding it incorrectly silently disables console
// events because the page frame is not the AppService context.
func TestConnectJsContextRequest(t *testing.T) {
	raw, err := EncodeConnectJsContext(9, "jc-1")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := Decode(raw)
	if err != nil || frame.Category != "connectJsContext" || frame.JsContext != "jc-1" {
		t.Fatal("connect request mismatch", frame, err)
	}
	if len(frame.Fields) != 1 || frame.Fields[0] != "1:2:4" {
		t.Fatal("unexpected connect fields", frame.Fields)
	}
	raw, err = EncodeConnectJsContext(10, "")
	if err != nil {
		t.Fatal(err)
	}
	if frame, err = Decode(raw); err != nil || frame.Category != "connectJsContext" || frame.JsContext != "" {
		t.Fatal("empty context request mismatch", frame, err)
	}
}
