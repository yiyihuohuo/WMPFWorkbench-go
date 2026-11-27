package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFindLatestFailedSession(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "data", "desktop", "sessions")
	older := filepath.Join(base, "aaaaaaaaaaaaaaaa")
	newer := filepath.Join(base, "bbbbbbbbbbbbbbbb")
	writeTestFile(t, filepath.Join(older, "failure.json"), `{"session_id":"aaaaaaaaaaaaaaaa","appid":"wxold","state":"error"}`)
	writeTestFile(t, filepath.Join(newer, "failure.json"), `{"session_id":"bbbbbbbbbbbbbbbb","appid":"wxnew","state":"error"}`)
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(older, "failure.json"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(newer, "failure.json"), newTime, newTime); err != nil {
		t.Fatal(err)
	}

	dir, failure, err := findLatestFailedSession(root)
	if err != nil {
		t.Fatal(err)
	}
	if dir != newer || diagnosticString(failure, "appid") != "wxnew" {
		t.Fatalf("wrong session selected: dir=%s failure=%#v", dir, failure)
	}
}

func TestCollectAndWriteDiagnosticZip(t *testing.T) {
	root := t.TempDir()
	tempLogs := filepath.Join(root, "temp")
	if err := os.MkdirAll(tempLogs, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tempLogs)
	t.Setenv("TMP", tempLogs)

	dir := filepath.Join(root, "data", "desktop", "sessions", "ccdd112233445566")
	writeTestFile(t, filepath.Join(dir, "observation", "events.jsonl"),
		"{\"event\":\"worker.started\",\"pid\":4321}\n{\"event\":\"script\",\"payload\":{\"event\":\"host.Reset\"}}\n")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{"session_id":"ccdd112233445566","appid":"wxde8ac0a21135c07d"}`)
	failure := map[string]any{
		"session_id": "ccdd112233445566",
		"appid":      "wxde8ac0a21135c07d",
		"reason":     "repeated_reset",
		"state":      "error",
		"failed_at":  "2026-11-27T12:59:06+08:00",
	}
	failureJSON, _ := json.Marshal(failure)
	writeTestFile(t, filepath.Join(dir, "failure.json"), string(failureJSON))
	writeTestFile(t, filepath.Join(dir, "inputs", "metadata.json"), `{"appid":"wxde8ac0a21135c07d"}`)
	agentLog := filepath.Join(tempLogs, "wmpf-agent-4321.log")
	writeTestFile(t, agentLog, "[diag] attach.host.Reset=0\n")
	failedAt, err := time.Parse(time.RFC3339, "2026-11-27T12:59:06+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(agentLog, failedAt, failedAt); err != nil {
		t.Fatal(err)
	}

	entries, manifest, err := collectDiagnosticEntries(root, dir, failure, []string{"12:59:06 启动失败"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{
		"README.txt", "manifest.json", "events.jsonl", "worker.json", "failure.json",
		"inputs/metadata.json", "agent/wmpf-agent-4321.log", "ui-log.txt",
	} {
		if !names[want] {
			t.Fatalf("missing %s in %#v", want, names)
		}
	}
	if manifest.SessionID != "ccdd112233445566" || manifest.AppID != "wxde8ac0a21135c07d" {
		t.Fatalf("bad manifest: %#v", manifest)
	}

	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	zipNames := map[string]string{}
	for _, f := range reader.File {
		rc, openErr := f.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		buf, readErr := io.ReadAll(rc)
		if readErr != nil {
			t.Fatal(readErr)
		}
		rc.Close()
		zipNames[f.Name] = string(buf)
	}
	if !strings.Contains(zipNames["events.jsonl"], "host.Reset") {
		t.Fatalf("events missing from zip: %q", zipNames["events.jsonl"])
	}
	if !strings.Contains(zipNames["README.txt"], "worker.started") {
		t.Fatalf("README missing analysis hint: %q", zipNames["README.txt"])
	}
	var decoded diagnosticManifest
	if err := json.Unmarshal([]byte(zipNames["manifest.json"]), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Reason != "repeated_reset" || len(decoded.Files) < 6 {
		t.Fatalf("bad manifest in zip: %#v", decoded)
	}
}

func TestReadDiagnosticFileTakesTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	writeTestFile(t, path, "0123456789")
	data, truncated, err := readDiagnosticFile(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || string(data) != "6789" {
		t.Fatalf("truncated=%v data=%q", truncated, data)
	}
}
