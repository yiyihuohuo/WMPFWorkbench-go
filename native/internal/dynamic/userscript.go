package dynamic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"time"
)

type UserScript struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Match      []string `json:"match"`
	RunAt      string   `json:"runAt"`
	InjectInto string   `json:"injectInto"`
	Enabled    bool     `json:"enabled"`
	SourceFile string   `json:"sourceFile"`
	CreatedAt  int64    `json:"createdAt"`
}

func newID(prefix string) string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return prefix + "-" + hex.EncodeToString(buf)
}

func (m *Manager) manifestPath() string { return m.path("userscripts.json") }

func (m *Manager) readScripts() ([]UserScript, error) {
	raw, err := os.ReadFile(m.manifestPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []UserScript{}, nil
		}
		return nil, err
	}
	var scripts []UserScript
	if err := json.Unmarshal(raw, &scripts); err != nil {
		return nil, err
	}
	if scripts == nil {
		scripts = []UserScript{}
	}
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].CreatedAt < scripts[j].CreatedAt })
	return scripts, nil
}

func (m *Manager) writeScripts(scripts []UserScript) error {
	raw, err := json.MarshalIndent(scripts, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.manifestPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.manifestPath())
}

func (m *Manager) findScript(id string) ([]UserScript, int, error) {
	scripts, err := m.readScripts()
	if err != nil {
		return nil, -1, err
	}
	for i := range scripts {
		if scripts[i].ID == id {
			return scripts, i, nil
		}
	}
	return scripts, -1, errors.New("脚本不存在")
}

func (m *Manager) UserscriptAdd(args map[string]any) (any, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	source, err := requireString(args, "source")
	if err != nil {
		return nil, err
	}
	match := []string{"*"}
	if raw, ok := args["match"].([]any); ok && len(raw) > 0 {
		match = []string{}
		for _, item := range raw {
			if text, ok := item.(string); ok && text != "" {
				match = append(match, text)
			}
		}
		if len(match) == 0 {
			match = []string{"*"}
		}
	}
	runAt, _ := args["runAt"].(string)
	if runAt == "" {
		runAt = "document_start"
	}
	injectInto, _ := args["injectInto"].(string)
	if injectInto == "" {
		injectInto = "page"
	}
	scripts, err := m.readScripts()
	if err != nil {
		return nil, err
	}
	script := UserScript{ID: newID("us"), Name: name, Match: match, RunAt: runAt,
		InjectInto: injectInto, Enabled: true, SourceFile: "", CreatedAt: time.Now().UnixMilli()}
	script.SourceFile = script.ID + ".js"
	if err := os.WriteFile(m.path(script.SourceFile), []byte(source), 0600); err != nil {
		return nil, err
	}
	scripts = append(scripts, script)
	if err := m.writeScripts(scripts); err != nil {
		return nil, err
	}
	return map[string]any{"script": script}, nil
}

func (m *Manager) UserscriptList() (any, error) {
	scripts, err := m.readScripts()
	if err != nil {
		return nil, err
	}
	return map[string]any{"scripts": scripts}, nil
}

func (m *Manager) UserscriptSetEnabled(id string, enabled bool) (any, error) {
	scripts, index, err := m.findScript(id)
	if err != nil {
		return nil, err
	}
	scripts[index].Enabled = enabled
	if err := m.writeScripts(scripts); err != nil {
		return nil, err
	}
	return map[string]any{"script": scripts[index]}, nil
}

func (m *Manager) UserscriptRemove(id string) (any, error) {
	scripts, index, err := m.findScript(id)
	if err != nil {
		return nil, err
	}
	target := scripts[index]
	scripts = append(scripts[:index], scripts[index+1:]...)
	if err := m.writeScripts(scripts); err != nil {
		return nil, err
	}
	_ = os.Remove(m.path(target.SourceFile))
	return map[string]any{"ok": true, "id": id}, nil
}

func (m *Manager) UserscriptReload(ctx context.Context) (any, error) {
	return m.reloadScripts(ctx, "")
}

// UserscriptReloadFor applies enabled scripts whose match pattern accepts the
// active appid. It is called after setupContext so new runtimes are covered.
func (m *Manager) UserscriptReloadFor(ctx context.Context, appid string) (any, error) {
	return m.reloadScripts(ctx, appid)
}

func (m *Manager) reloadScripts(ctx context.Context, appid string) (any, error) {
	scripts, err := m.readScripts()
	if err != nil {
		return nil, err
	}
	applied, identifiers := 0, []string{}
	for _, script := range scripts {
		if !script.Enabled || (appid != "" && !matchScript(script, appid)) {
			continue
		}
		raw, err := os.ReadFile(m.path(script.SourceFile))
		if err != nil {
			continue
		}
		source := "(function(){\n" + string(raw) + "\n})();"
		id, err := m.AddScript(ctx, source)
		if err != nil {
			continue
		}
		if id == "" {
			id = script.ID
		}
		identifiers = append(identifiers, id)
		if _, err := m.Eval(ctx, source); err == nil {
			applied++
		}
	}
	return map[string]any{"applied": applied, "identifiers": identifiers}, nil
}

func matchScript(script UserScript, appid string) bool {
	for _, pattern := range script.Match {
		pattern = strings.TrimSpace(pattern)
		if pattern == "*" || pattern == appid {
			return true
		}
	}
	return false
}
