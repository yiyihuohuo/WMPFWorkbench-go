package cdp

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionWebsocketResponsesEventsAndClose(t *testing.T) {
	upgrade := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			var m map[string]any
			if ws.ReadJSON(&m) != nil {
				return
			}
			ws.WriteJSON(map[string]any{"method": "Runtime.consoleAPICalled", "params": map[string]any{"type": "log"}})
			ws.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{"value": 42}})
		}
	}))
	defer srv.Close()
	s, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.Send(context.Background(), "Runtime.evaluate", map[string]any{"expression": "6*7"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	json.Unmarshal(raw, &value)
	if value["value"] != float64(42) {
		t.Fatal(string(raw))
	}
	page, _ := s.Buffer.Read(context.Background(), ReadOptions{})
	if len(page.Events) != 1 {
		t.Fatal(page)
	}
	s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = s.Send(ctx, "Runtime.evaluate", nil, ""); err == nil {
		t.Fatal("accepted closed session")
	}
}
