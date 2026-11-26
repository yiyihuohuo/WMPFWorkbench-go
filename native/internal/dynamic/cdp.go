// Package dynamic implements runtime analysis features on top of the existing
// CDP bridge: persistent userscripts, captured API calls, route maps, and scan
// reports. It intentionally contains no Frida code and no hardcoded offsets.
package dynamic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

type CDP interface {
	Send(context.Context, string, any, string) (json.RawMessage, error)
}

type Manager struct {
	root string
	cdp  func() CDP
	mu   sync.Mutex
}

func New(root string, cdp func() CDP) *Manager {
	_ = os.MkdirAll(root, 0700)
	return &Manager{root: root, cdp: cdp}
}

func (m *Manager) path(name string) string { return filepath.Join(m.root, name) }

func (m *Manager) client() (CDP, error) {
	if m == nil || m.cdp == nil {
		return nil, errors.New("CDP 未连接")
	}
	c := m.cdp()
	if c == nil {
		return nil, errors.New("CDP 未连接")
	}
	value := reflect.ValueOf(c)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return nil, errors.New("CDP 未连接")
	}
	return c, nil
}

func cdpValue(raw json.RawMessage) (any, error) {
	var response struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if len(response.Result.Value) == 0 || string(response.Result.Value) == "null" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(response.Result.Value, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func (m *Manager) Eval(ctx context.Context, expression string) (any, error) {
	c, err := m.client()
	if err != nil {
		return nil, err
	}
	raw, err := c.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": false,
	}, "")
	if err != nil {
		return nil, err
	}
	return cdpValue(raw)
}

func (m *Manager) AddScript(ctx context.Context, source string) (string, error) {
	c, err := m.client()
	if err != nil {
		return "", err
	}
	raw, err := c.Send(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": source}, "")
	if err != nil {
		return "", err
	}
	var response struct {
		Result struct {
			Identifier string `json:"identifier"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", err
	}
	return response.Result.Identifier, nil
}

func jsString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func jsObject(value any) string {
	if value == nil {
		return "{}"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func objectArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range args {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func requireString(args map[string]any, key string) (string, error) {
	value, _ := args[key].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s 不能为空", key)
	}
	return value, nil
}
