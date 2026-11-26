package netlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTruncatedNetlogRecoversCompleteEventsWithFullQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netlog.json")
	raw := `{"constants":{"logEventTypes":{"URL_REQUEST":999999999}},"events":[{"time":"1","type":999999999,"params":{"url":"https://api.example.test/path?token=secret&x=2","method":"GET"}}, {"time":"2"`
	os.WriteFile(path, []byte(raw), 0600)
	report, err := Analyze(path)
	if err != nil {
		t.Fatal(err)
	}
	summary := report["summary"].(map[string]any)
	if summary["events"] != 1 || summary["truncated_salvage"] != true {
		t.Fatalf("summary: %+v", summary)
	}
	b, _ := json.Marshal(report)
	if !strings.Contains(string(b), "token=secret") || !strings.Contains(string(b), "URL_REQUEST") {
		t.Fatalf("full URL query missing: %s", b)
	}
}

func TestCompleteOldStyleNetlogAndEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netlog.json")
	os.WriteFile(path, []byte(`{"constants":{"event_type":["URL_REQUEST"]},"events":[{"type":0,"extra":{"url":"https://api.weixin.qq.com/v1","http_status_code":200}}]}`), 0600)
	report, err := Analyze(path)
	if err != nil || report["summary"].(map[string]any)["events"] != 1 {
		t.Fatalf("report %+v %v", report, err)
	}
	os.WriteFile(path, nil, 0600)
	report, err = Analyze(path)
	if err != nil || report["summary"].(map[string]any)["empty"] != true {
		t.Fatalf("empty: %+v %v", report, err)
	}
}
