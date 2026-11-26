package dynamic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeCDP struct {
	methods []string
	sources []string
}

func (f *fakeCDP) Send(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, error) {
	f.methods = append(f.methods, method)
	if row, ok := params.(map[string]any); ok {
		if source, ok := row["source"].(string); ok {
			f.sources = append(f.sources, source)
		}
	}
	if method == "Page.addScriptToEvaluateOnNewDocument" {
		return json.RawMessage(`{"result":{"identifier":"id-1"}}`), nil
	}
	return json.RawMessage(`{"result":{"value":null}}`), nil
}

func TestUserscriptLifecycle(t *testing.T) {
	fake := &fakeCDP{}
	m := New(t.TempDir(), func() CDP { return fake })
	added, err := m.UserscriptAdd(map[string]any{"name": "probe", "source": "window.__probe=1;", "match": []any{"wxTEST"}})
	if err != nil {
		t.Fatal(err)
	}
	row := added.(map[string]any)["script"].(UserScript)
	if !row.Enabled || len(row.Match) != 1 || row.Match[0] != "wxTEST" {
		t.Fatalf("script=%+v", row)
	}
	listed, _ := m.UserscriptList()
	raw, _ := json.Marshal(listed)
	if string(raw) == "" {
		t.Fatal("empty list")
	}
	if _, err := m.UserscriptSetEnabled(row.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UserscriptReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.methods) != 0 {
		t.Fatalf("disabled script was injected: %v", fake.methods)
	}
	if _, err := m.UserscriptSetEnabled(row.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UserscriptReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.methods) == 0 || fake.methods[0] != "Page.addScriptToEvaluateOnNewDocument" {
		t.Fatalf("persistent script not installed: %v", fake.methods)
	}
	if _, err := m.UserscriptRemove(row.ID); err != nil {
		t.Fatal(err)
	}
}

func TestMatchScript(t *testing.T) {
	s := UserScript{Match: []string{"*", "wxTEST"}}
	if !matchScript(s, "wxOTHER") || !matchScript(s, "wxTEST") {
		t.Fatal("wildcard or exact match failed")
	}
	s.Match = []string{"wxTEST"}
	if matchScript(s, "wxOTHER") {
		t.Fatal("unexpected match")
	}
}

func TestUserscriptReloadForFiltersMatch(t *testing.T) {
	fake := &fakeCDP{}
	m := New(t.TempDir(), func() CDP { return fake })
	_, _ = m.UserscriptAdd(map[string]any{"name": "match", "source": "window.__match=1;", "match": []any{"wxTEST"}})
	_, _ = m.UserscriptAdd(map[string]any{"name": "other", "source": "window.__other=1;", "match": []any{"wxOTHER"}})
	if _, err := m.UserscriptReloadFor(context.Background(), "wxTEST"); err != nil {
		t.Fatal(err)
	}
	if len(fake.sources) != 1 || !strings.Contains(fake.sources[0], "__match") {
		t.Fatalf("sources=%v", fake.sources)
	}
}
