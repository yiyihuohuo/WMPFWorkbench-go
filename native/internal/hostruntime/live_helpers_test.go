package hostruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// consoleMarkerSeen only accepts an event carrying this run's unique marker.
// Startup-time console logs from the mini-program itself must not satisfy it.
func consoleMarkerSeen(events []map[string]any, marker string) bool {
	for _, event := range events {
		if event["method"] != "Runtime.consoleAPICalled" {
			continue
		}
		params, _ := event["params"].(map[string]any)
		if params == nil {
			continue
		}
		args, _ := params["args"].([]any)
		for _, arg := range args {
			switch value := arg.(type) {
			case string:
				if strings.Contains(value, marker) {
					return true
				}
			case map[string]any:
				if strings.Contains(fmt.Sprint(value["value"]), marker) {
					return true
				}
			}
		}
	}
	return false
}

// readBridgeMethods summarizes the bridge-side layer of the four-layer
// evidence chain (upstream frame -> bridge.event -> cdp pump -> Buffer.Read).
func readBridgeMethods(path string) (map[string]int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			Event   string `json:"event"`
			Payload struct {
				Method string `json:"method"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &row) == nil && row.Event == "bridge.event" {
			out[row.Payload.Method]++
		}
	}
	return out, nil
}
