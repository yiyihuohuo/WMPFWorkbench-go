package contract

import (
	"encoding/json"
	"testing"
)

func TestFrozenCatalog(t *testing.T) {
	tools, resources, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 40 || len(resources) != 4 {
		t.Fatalf("catalog: %d tools / %d resources", len(tools), len(resources))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool.Name] || tool.Name == "source_export" {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
		seen[tool.Name] = true
		var schema map[string]any
		if json.Unmarshal(tool.InputSchema, &schema) != nil || schema["type"] != "object" {
			t.Fatalf("invalid schema %s", tool.Name)
		}
	}
	if !seen["cdp_events"] {
		t.Fatal("missing event tool")
	}
}
