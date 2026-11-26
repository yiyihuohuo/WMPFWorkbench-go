// Package contract freezes the existing MCP surface independently of SDK schema inference.
package contract

import (
	"embed"
	"encoding/json"
)

//go:embed data/*.json
var data embed.FS

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Resource struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Title       string `json:"title,omitempty"`
}

func Catalog() ([]Tool, []Resource, error) {
	var tools []Tool
	var resources []Resource
	raw, err := data.ReadFile("data/tools.json")
	if err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(raw, &tools); err != nil {
		return nil, nil, err
	}
	raw, err = data.ReadFile("data/resources.json")
	if err != nil {
		return nil, nil, err
	}
	err = json.Unmarshal(raw, &resources)
	return tools, resources, err
}

func References() (map[string]json.RawMessage, error) {
	raw, err := data.ReadFile("data/references.json")
	if err != nil {
		return nil, err
	}
	var refs map[string]json.RawMessage
	err = json.Unmarshal(raw, &refs)
	return refs, err
}
