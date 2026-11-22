package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"wmpfworkbench/contract"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/core"
)

func TestFrozenCatalogAndLivePolicy(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	p.Roots = nil
	c := core.New(p)
	defer c.Close()
	srv, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "migration-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 17 {
		t.Fatalf("tools: %+v %v", list, err)
	}
	frozen, _, _ := contract.Catalog()
	schemas := map[string]json.RawMessage{}
	for _, tool := range frozen {
		schemas[tool.Name] = tool.InputSchema
	}
	for _, tool := range list.Tools {
		got, _ := json.Marshal(tool.InputSchema)
		var a, b any
		json.Unmarshal(got, &a)
		json.Unmarshal(schemas[tool.Name], &b)
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if string(x) != string(y) {
			t.Fatalf("schema drift: %s", tool.Name)
		}
	}
	resources, err := cs.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 4 {
		t.Fatalf("resources: %+v %v", resources, err)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "index", Arguments: map[string]any{"action": "stats"}})
	if err != nil || result.IsError {
		t.Fatalf("index %+v %v", result, err)
	}
	c.Config.Update(ctx, map[string]any{"disabled_tools": []string{"index"}})
	list, err = cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 16 {
		t.Fatalf("policy list %+v %v", list, err)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "index"})
	if err == nil && !result.IsError {
		t.Fatal("disabled tool was callable")
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "source_export"})
	if err == nil && !result.IsError {
		t.Fatal("retired tool was callable")
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "extract_api_surface", Arguments: map[string]any{"appid": 123}})
	if err == nil && !result.IsError {
		t.Fatal("wrong type accepted")
	}
	c.Config.Update(ctx, map[string]any{"mcp_enabled": false})
	list, err = cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 0 {
		t.Fatalf("global policy %+v %v", list, err)
	}
}
func TestHTTPRequiresBearerOnAllRoutes(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	p.Roots = nil
	c := core.New(p)
	defer c.Close()
	srv, _ := New(c)
	h, err := HTTP(c, srv, "fixture-token", func() {})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	for _, path := range []string{"/mcp", "/console/health", "/console/tools", "/console/sessions"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("unprotected: %s %d", path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", server.URL+"/console/tools", nil)
	req.Header.Set("Authorization", "Bearer fixture-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("authorized %d", resp.StatusCode)
	}
	if _, err = HTTP(c, srv, "", func() {}); err == nil {
		t.Fatal("empty token allowed")
	}
}
