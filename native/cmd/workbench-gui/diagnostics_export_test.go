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
	olderFailure := filepath.Join(older, "observation", "failure.json")
	newerFailure := filepath.Join(newer, "observation", "failure.json")
	writeTestFile(t, olderFailure, `{"session_id":"aaaaaaaaaaaaaaaa","appid":"wxold","state":"error"}`)
	writeTestFile(t, newerFailure, `{"session_id":"bbbbbbbbbbbbbbbb","appid":"wxnew","state":"error"}`)
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(olderFailure, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newerFailure, newTime, newTime); err != nil {
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

func TestFindLatestFailedSessionFallsBackToLegacyPath(t *testing.T) {
	root := t.TempDir()
	sess := filepath.Join(root, "data", "desktop", "sessions", "eeeeeeeeeeeeeeee")
	legacy := filepath.Join(sess, "failure.json")
	writeTestFile(t, legacy, `{"session_id":"eeeeeeeeeeeeeeee","appid":"wxlegacy","state":"error"}`)

	dir, failure, err := findLatestFailedSession(root)
	if err != nil {
		t.Fatal(err)
	}
	if dir != sess || diagnosticString(failure, "appid") != "wxlegacy" {
		t.Fatalf("legacy failure not found: dir=%s failure=%#v", dir, failure)
	}

	preferred := filepath.Join(sess, "observation", "failure.json")
	writeTestFile(t, preferred, `{"session_id":"eeeeeeeeeeeeeeee","appid":"wxlegacy","state":"error"}`)
	if got := diagnosticFailurePath(sess); got != preferred {
		t.Fatalf("observation path should win: %s", got)
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
		"{\"event\":\"worker.started\",\"pid\":4321}\n{\"event\":\"host.stdout\",\"payload\":{\"line\":\"[authcb] GetUserInfo uin=25985012628405866 status=2\"}}\n{\"event\":\"script\",\"payload\":{\"event\":\"host.Reset\"}}\n")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{"session_id":"ccdd112233445566","appid":"wxde8ac0a21135c07d"}`)
	failure := map[string]any{
		"session_id": "ccdd112233445566",
		"appid":      "wxde8ac0a21135c07d",
		"reason":     "repeated_reset",
		"state":      "error",
		"failed_at":  "2026-11-27T12:59:06+08:00",
	}
	failureJSON, _ := json.Marshal(failure)
	writeTestFile(t, filepath.Join(dir, "observation", "failure.json"), string(failureJSON))
	writeTestFile(t, filepath.Join(dir, "inputs", "metadata.json"), `{"appid":"wxde8ac0a21135c07d"}`)
	agentLog := filepath.Join(tempLogs, "wmpf-agent-4321.log")
	writeTestFile(t, agentLog, "[diag] attach.host.Reset=0 openid=owabcdefghijklmnopqrst token=SUPERSECRET_TOKEN\n")
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
	manifestBytes, _ := json.Marshal(manifest)
	for _, sensitive := range []string{"25985012628405866", "SUPERSECRET_TOKEN", "owabcdefghijklmnopqrst", "buhuo"} {
		for _, e := range entries {
			if strings.Contains(string(e.data), sensitive) {
				t.Fatalf("entry %s still contains %q: %s", e.Name, sensitive, e.data)
			}
		}
		if strings.Contains(string(manifestBytes), sensitive) {
			t.Fatalf("manifest still contains %q: %s", sensitive, manifestBytes)
		}
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

func TestCollectWithoutEventsFileStillExports(t *testing.T) {
	root := t.TempDir()
	tempLogs := filepath.Join(root, "temp")
	if err := os.MkdirAll(tempLogs, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tempLogs)
	t.Setenv("TMP", tempLogs)

	dir := filepath.Join(root, "data", "desktop", "sessions", "ffffffffffffffff")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{"session_id":"ffffffffffffffff","appid":"wxmissing"}`)
	failure := map[string]any{
		"session_id": "ffffffffffffffff",
		"appid":      "wxmissing",
		"reason":     "repeated_reset",
		"state":      "error",
		"failed_at":  "2026-11-27T20:21:08+08:00",
	}
	failureJSON, _ := json.Marshal(failure)
	writeTestFile(t, filepath.Join(dir, "observation", "failure.json"), string(failureJSON))

	entries, manifest, err := collectDiagnosticEntries(root, dir, failure, nil)
	if err != nil {
		t.Fatalf("missing events.jsonl must not abort export: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"README.txt", "manifest.json", "missing.txt", "failure.json", "worker.json"} {
		if !names[want] {
			t.Fatalf("missing %s in %#v", want, names)
		}
	}
	found := false
	for _, m := range manifest.Missing {
		if strings.HasPrefix(m, "events.jsonl <-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("events.jsonl absence not recorded: %#v", manifest.Missing)
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
	missingEntry := ""
	for _, f := range reader.File {
		if f.Name != "missing.txt" {
			continue
		}
		rc, openErr := f.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		buf, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		missingEntry = string(buf)
	}
	if !strings.Contains(missingEntry, "events.jsonl") {
		t.Fatalf("missing.txt lacks events.jsonl: %q", missingEntry)
	}
}

func TestRedactDiagnosticText(t *testing.T) {
	root := `E:\work\app`
	temp := `C:\Users\alice\AppData\Local\Temp`
	input := `uin=12345678901234567 token=abc openid=owabcdefghijklmnopqrst path=` + root + `\data\config.json temp=` + temp + `\x.log`
	out := redactDiagnosticText(input, root, temp)
	for _, sensitive := range []string{"12345678901234567", "abc", "owabcdefghijklmnopqrst", `E:\work\app`, `C:\Users\alice`} {
		if strings.Contains(out, sensitive) {
			t.Fatalf("redaction left %q in %q", sensitive, out)
		}
	}
	for _, marker := range []string{"uin=***", "token=***", "<APP_ROOT>", "<TEMP>"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("redaction lost marker %s in %q", marker, out)
		}
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
