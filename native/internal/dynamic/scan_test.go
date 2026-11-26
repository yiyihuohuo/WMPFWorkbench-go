package dynamic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRulesAndReport(t *testing.T) {
	m := New(t.TempDir(), func() CDP { return &fakeCDP{} })
	listed, err := m.ScanRules(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.(map[string]any)["rules"].([]ScanRule)) < 20 {
		t.Fatal("default scan rule set is too small")
	}
	added, err := m.ScanRules(map[string]any{"action": "add", "name": "test", "pattern": `TESTKEY_[0-9]+`, "severity": "low"})
	if err != nil {
		t.Fatal(err)
	}
	rule := added.(map[string]any)["rule"].(ScanRule)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.js"), []byte("const k='TESTKEY_12345';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := m.ScanRun(map[string]any{"path": source})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["count"].(int) == 0 {
		t.Fatal("no findings")
	}
	findings := result.(map[string]any)["findings"].([]Finding)
	if findings[0].Match != "TESTKEY_12345" {
		t.Fatalf("raw match was damaged: %+v", findings[0])
	}
	if _, err := m.ScanRules(map[string]any{"action": "update", "id": rule.ID, "name": "updated", "pattern": `TESTKEY_[0-9]{5}`, "enabled": false}); err != nil {
		t.Fatal(err)
	}
	report := result.(map[string]any)["report"].(string)
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "敏感信息扫描报告") || !strings.Contains(string(raw), "代码上下文") {
		t.Fatalf("bad report: %s", raw)
	}
	if _, err := m.ScanRules(map[string]any{"action": "remove", "id": rule.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestNoiseSecretFilter(t *testing.T) {
	if !isNoiseSecret(`TOKEN:"/1/wechatApp/getAccessToken"`) {
		t.Fatal("endpoint path must be ignored")
	}
	if isNoiseSecret(`token:"abcd1234SECUREvalue"`) {
		t.Fatal("real token must not be ignored")
	}
}
