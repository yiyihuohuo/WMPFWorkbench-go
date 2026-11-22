package mcpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"strings"
	"time"
	"wmpfworkbench/contract"
	"wmpfworkbench/internal/core"
)

func result(value any, err error) *mcp.CallToolResult {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return result(nil, errors.New("结果编码失败"))
	}
	var structured map[string]any
	if json.Unmarshal(raw, &structured) != nil {
		return result(nil, errors.New("工具结果必须是对象"))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: structured}
}
func retired(name string) string {
	switch name {
	case "source_export", "dump_source", "dump_source_via_cdp":
		return "Tool retired; refresh tools/list. Use decrypt_wxapkg for package source, get_app_detail/index for local materials, cdp_scripts for reported scripts."
	case "sdk_abi_notes", "xweb_relay_contract", "netlog_trigger_spec":
		return "Reference moved to MCP resources; refresh resources/list"
	case "search_local_mini_programs", "rebuild_index", "browse_indexed", "index_stats", "list_packages", "host_status", "runtime_sessions", "list_running", "cdp_enable", "cdp_get_script_source", "netlog_find_existing", "netlog_summarize", "netlog_api_surface":
		return "Tool retired; refresh tools/list"
	}
	return ""
}
func New(c *core.Service) (*mcp.Server, error) {
	tools, resources, err := contract.Catalog()
	if err != nil {
		return nil, err
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "wmpf", Version: "0.1.0-native"}, &mcp.ServerOptions{HasTools: true, HasResources: true})
	for _, frozen := range tools {
		tool := frozen
		var schema jsonschema.Schema
		if err = json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return nil, err
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, err
		}
		s.AddTool(&mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			raw := req.Params.Arguments
			if len(raw) == 0 {
				raw = []byte("{}")
			}
			if json.Unmarshal(raw, &args) != nil || args == nil {
				return result(nil, errors.New("arguments 必须是对象")), nil
			}
			if err := resolved.Validate(args); err != nil {
				return result(nil, errors.New("工具参数类型、必填字段或枚举值不正确")), nil
			}
			// Apply the frozen defaults without re-inferring a schema from Go structs.
			var data map[string]any
			json.Unmarshal(tool.InputSchema, &data)
			props, _ := data["properties"].(map[string]any)
			for key, v := range props {
				if _, exists := args[key]; !exists {
					if p, ok := v.(map[string]any); ok {
						if value, ok := p["default"]; ok {
							args[key] = value
						}
					}
				}
			}
			// All tools return promptly: export_mini_program only enqueues a
			// task and export_status is a quick poll, so no long special-case.
			ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
			defer cancel()
			value, err := c.Call(ctx, tool.Name, args)
			return result(value, err), nil
		})
	}
	for _, entry := range resources {
		r := entry
		s.AddResource(&mcp.Resource{Name: r.Name, URI: r.URI, Title: r.Title, Description: r.Description, MIMEType: r.MIMEType}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			value, err := c.Resource(r.URI)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.URI, MIMEType: r.MIMEType, Text: string(raw)}}}, nil
		})
	}
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				call, ok := req.(*mcp.CallToolRequest)
				if !ok {
					return nil, errors.New("invalid tools/call")
				}
				if message := retired(call.Params.Name); message != "" {
					return result(nil, errors.New(message)), nil
				}
				if !c.Config.Enabled(call.Params.Name) {
					return result(nil, errors.New("Tool disabled by desktop configuration: "+call.Params.Name)), nil
				}
			}
			value, err := next(ctx, method, req)
			if method == "tools/list" && err == nil {
				if list, ok := value.(*mcp.ListToolsResult); ok {
					filtered := []*mcp.Tool{}
					for _, tool := range list.Tools {
						if c.Config.Enabled(tool.Name) {
							filtered = append(filtered, tool)
						}
					}
					copy := *list
					copy.Tools = filtered
					return &copy, nil
				}
			}
			return value, err
		}
	})
	return s, nil
}
func HTTP(c *core.Service, s *mcp.Server, token string, shutdown func()) (http.Handler, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("desktop MCP access token missing")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("GET /console/health", func(w http.ResponseWriter, r *http.Request) {
		tools, _, _ := contract.Catalog()
		count := 0
		for _, t := range tools {
			if c.Config.Enabled(t.Name) {
				count++
			}
		}
		write(w, map[string]any{"ok": true, "tools": count, "service": "wmpf-console"})
	})
	mux.HandleFunc("GET /console/tools", func(w http.ResponseWriter, r *http.Request) {
		tools, _, _ := contract.Catalog()
		rows := []any{}
		for _, t := range tools {
			rows = append(rows, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema, "enabled": c.Config.Enabled(t.Name)})
		}
		write(w, map[string]any{"tools": rows})
	})
	mux.HandleFunc("GET /console/sessions", func(w http.ResponseWriter, r *http.Request) { write(w, map[string]any{"sessions": c.Sessions()}) })
	mux.HandleFunc("POST /console/shutdown", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"stopping": true})
		if shutdown != nil {
			go shutdown()
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
