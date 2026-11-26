package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type peer struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (p *peer) send(kind int, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return p.ws.WriteMessage(kind, data)
}

type route struct {
	client   *peer
	target   *peer
	original json.RawMessage
	timer    *time.Timer
}
type Server struct {
	targetListener, cdpListener net.Listener
	targetServer, cdpServer     *http.Server
	mu                          sync.Mutex
	targets, clients            map[*peer]bool
	activeTarget                *peer
	targetOrder                 map[*peer]uint64
	targetSeq                   uint64
	routes                      map[uint64]*route
	counts                      map[string]int
	id, seq                     atomic.Uint64
	closeOnce                   sync.Once
	log                         func(string, map[string]any)
	done                        chan struct{}
}

func Start(ctx context.Context, targetAddr, cdpAddr string, log func(string, map[string]any)) (*Server, error) {
	first, err := net.Listen("tcp", targetAddr)
	if err != nil {
		return nil, err
	}
	second, err := net.Listen("tcp", cdpAddr)
	if err != nil {
		first.Close()
		return nil, err
	}
	if log == nil {
		log = func(string, map[string]any) {}
	}
	b := &Server{targetListener: first, cdpListener: second, targets: map[*peer]bool{}, targetOrder: map[*peer]uint64{}, clients: map[*peer]bool{}, routes: map[uint64]*route{}, counts: map[string]int{}, log: log, done: make(chan struct{})}
	b.id.Store(1000000)
	b.targetServer = &http.Server{Handler: http.HandlerFunc(b.acceptTarget), ReadHeaderTimeout: 5 * time.Second}
	b.cdpServer = &http.Server{Handler: http.HandlerFunc(b.acceptClient), ReadHeaderTimeout: 5 * time.Second}
	go b.targetServer.Serve(first)
	go b.cdpServer.Serve(second)
	go func() {
		select {
		case <-ctx.Done():
			b.Close()
		case <-b.done:
		}
	}()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-b.done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.emitStats(false)
			}
		}
	}()
	b.log("bridge.ready", map[string]any{"debug_address": first.Addr().String(), "cdp_address": second.Addr().String()})
	return b, nil
}
func (b *Server) TargetAddress() string { return b.targetListener.Addr().String() }
func (b *Server) CDPAddress() string    { return b.cdpListener.Addr().String() }
func (b *Server) Pending() int          { b.mu.Lock(); defer b.mu.Unlock(); return len(b.routes) }

// count and Stats expose what actually crossed the bridge boundary. They are
// diagnostic metadata only: no CDP payload, ticket or credential is recorded.
func (b *Server) count(key string) int {
	b.mu.Lock()
	b.counts[key]++
	n := b.counts[key]
	b.mu.Unlock()
	return n
}
func (b *Server) snapshot() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int, len(b.counts))
	for k, v := range b.counts {
		out[k] = v
	}
	return out
}
func (b *Server) Stats() map[string]int { return b.snapshot() }
func (b *Server) emitStats(final bool) {
	b.log("bridge.stats", map[string]any{"counts": b.snapshot(), "final": final})
}
func (b *Server) Close() {
	first := false
	b.closeOnce.Do(func() {
		first = true
		close(b.done)
		b.targetServer.Close()
		b.cdpServer.Close()
		b.mu.Lock()
		defer b.mu.Unlock()
		for p := range b.targets {
			p.ws.Close()
		}
		for p := range b.clients {
			p.ws.Close()
		}
		for _, r := range b.routes {
			r.timer.Stop()
		}
		b.routes = map[uint64]*route{}
	})
	if first {
		b.emitStats(true)
	}
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func upgrade(w http.ResponseWriter, r *http.Request) (*peer, error) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		http.Error(w, "loopback only", 403)
		return nil, errors.New("non-local peer")
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrame)
	return &peer{ws: ws}, nil
}
func (b *Server) acceptTarget(w http.ResponseWriter, r *http.Request) {
	p, err := upgrade(w, r)
	if err != nil {
		return
	}
	b.mu.Lock()
	b.targets[p] = true
	b.targetSeq++
	b.targetOrder[p] = b.targetSeq
	if b.activeTarget == nil {
		b.activeTarget = p
	}
	b.mu.Unlock()
	defer func() {
		p.ws.Close()
		b.mu.Lock()
		delete(b.targets, p)
		delete(b.targetOrder, p)
		wasActive := b.activeTarget == p
		if wasActive {
			b.activeTarget = nil
			var newest uint64
			for candidate, order := range b.targetOrder {
				if b.targets[candidate] && order > newest {
					newest, b.activeTarget = order, candidate
				}
			}
		}
		failed := []*route{}
		for id, row := range b.routes {
			if row.target == p {
				row.timer.Stop()
				delete(b.routes, id)
				failed = append(failed, row)
			}
		}
		b.mu.Unlock()
		for _, row := range failed {
			sendError(row.client, row.original, -32000, "CDP target disconnected")
		}
		if wasActive && b.activeTarget == nil {
			b.log("miniapp.disconnected", map[string]any{"was_active": true})
		} else {
			b.log("miniapp.target_disconnected", map[string]any{"was_active": wasActive, "fallback_active": b.activeTarget != nil})
		}
	}()
	b.log("miniapp.connected", nil)
	for {
		_, raw, err := p.ws.ReadMessage()
		if err != nil {
			return
		}
		frame, err := Decode(raw)
		if err != nil {
			b.count("decode_error")
			b.log("bridge.decode_error", nil)
			continue
		}
		b.count("category:" + frame.Category)
		switch frame.Category {
		case "setupContext":
			b.mu.Lock()
			if b.targets[p] {
				b.targetSeq++
				b.targetOrder[p] = b.targetSeq
				b.activeTarget = p
			}
			b.mu.Unlock()
			b.log("miniapp.setup", map[string]any{"active": true})
			// The page frame is not the AppService context; attach it explicitly
			// before enabling Runtime domains.
			if encoded, err := EncodeConnectJsContext(b.seq.Add(1), ""); err == nil {
				b.count("setup_command:connectJsContext")
				b.log("bridge.connect_request", map[string]any{"jscontext_id": ""})
				p.send(websocket.BinaryMessage, encoded)
			}
			for _, method := range []string{"Runtime.enable", "Runtime.runIfWaitingForDebugger", "Debugger.enable"} {
				id := b.id.Add(1)
				raw, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": map[string]any{}})
				encoded, _ := Encode(b.seq.Add(1), "chromeDevtools", string(raw))
				b.count("setup_command:" + method)
				b.log("bridge.setup_command", map[string]any{"method": method, "id": id})
				p.send(websocket.BinaryMessage, encoded)
			}
		case "chromeDevtoolsResult":
			if len(frame.Payload) == 0 {
				b.count("empty_result")
				b.log("bridge.empty_result", nil)
				continue
			}
			b.upstream(p, []byte(frame.Payload), frameMeta{Inner: frame.Fields, Outer: frame.Outer, Bytes: len(raw)})
		case "addJsContext", "removeJsContext", "connectJsContext":
			b.count("event:" + frame.Category)
			b.log("bridge.jscontext", map[string]any{"category": frame.Category, "jscontext_id": frame.JsContext, "fields": frame.Fields})
			if frame.Category == "addJsContext" && frame.JsContext != "" {
				if encoded, err := EncodeConnectJsContext(b.seq.Add(1), frame.JsContext); err == nil {
					b.count("setup_command:connectJsContext")
					b.log("bridge.connect_request", map[string]any{"jscontext_id": frame.JsContext})
					p.send(websocket.BinaryMessage, encoded)
				}
			}
		default:
			if frame.Category != "pong" {
				b.log("miniapp.category", map[string]any{"category": frame.Category})
			}
		}
	}
}
func (b *Server) acceptClient(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case "/", "/json/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Browser": "WMPF owned runtime", "Protocol-Version": "1.3",
				"webSocketDebuggerUrl": "ws://" + r.Host,
			})
			return
		case "/json/list":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "owned-runtime", "type": "page", "title": "WMPF owned runtime",
				"url": "ws://" + r.Host, "webSocketDebuggerUrl": "ws://" + r.Host,
			}})
			return
		}
	}
	p, err := upgrade(w, r)
	if err != nil {
		return
	}
	b.mu.Lock()
	b.clients[p] = true
	b.mu.Unlock()
	defer func() {
		p.ws.Close()
		b.mu.Lock()
		delete(b.clients, p)
		for id, row := range b.routes {
			if row.client == p {
				row.timer.Stop()
				delete(b.routes, id)
			}
		}
		b.mu.Unlock()
	}()
	for {
		_, raw, err := p.ws.ReadMessage()
		if err != nil {
			return
		}
		b.downstream(p, raw)
	}
}
func sendError(p *peer, id json.RawMessage, code int, message string) {
	raw, _ := json.Marshal(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
	p.send(websocket.TextMessage, raw)
}
func (b *Server) downstream(p *peer, raw []byte) {
	var message map[string]json.RawMessage
	if json.Unmarshal(raw, &message) != nil {
		return
	}
	var method string
	json.Unmarshal(message["method"], &method)
	if method == "" {
		return
	}
	b.count("command:" + method)
	original := message["id"]
	if method == "Page.getFrameTree" {
		sendError(p, original, -32601, "Page.getFrameTree disabled: current runtime frame filter can crash")
		return
	}
	b.mu.Lock()
	target := b.activeTarget
	if target == nil || !b.targets[target] {
		b.mu.Unlock()
		sendError(p, original, -32000, "CDP runtime not connected")
		return
	}
	if len(original) > 0 {
		if len(b.routes) >= 4096 {
			b.mu.Unlock()
			sendError(p, original, -32000, "CDP pending limit exceeded")
			return
		}
		id := b.id.Add(1)
		message["id"], _ = json.Marshal(id)
		timer := time.AfterFunc(120*time.Second, func() {
			b.mu.Lock()
			row := b.routes[id]
			delete(b.routes, id)
			b.mu.Unlock()
			if row != nil {
				sendError(row.client, row.original, -32000, "CDP target response timed out")
			}
		})
		b.routes[id] = &route{client: p, target: target, original: append(json.RawMessage{}, original...), timer: timer}
	}
	b.mu.Unlock()
	payload, _ := json.Marshal(message)
	encoded, err := Encode(b.seq.Add(1), "chromeDevtools", string(payload))
	if err != nil {
		return
	}
	target.send(websocket.BinaryMessage, encoded)
}

type frameMeta struct {
	Inner []string
	Outer []string
	Bytes int
}

func (b *Server) upstream(source *peer, raw []byte, meta frameMeta) {
	var message map[string]json.RawMessage
	if json.Unmarshal(raw, &message) != nil {
		b.count("upstream_parse_error")
		b.log("bridge.parse_error", nil)
		return
	}
	if original := message["id"]; len(original) > 0 {
		var id uint64
		if json.Unmarshal(original, &id) != nil {
			b.count("upstream_bad_id")
			return
		}
		b.mu.Lock()
		row := b.routes[id]
		if row != nil && row.target == source {
			delete(b.routes, id)
		} else if row != nil {
			b.mu.Unlock()
			b.count("stale_response")
			b.log("bridge.stale_response", map[string]any{"id": id})
			return
		}
		b.mu.Unlock()
		if row == nil {
			b.count("unrouted")
			b.log("bridge.unrouted", map[string]any{"id": id})
			return
		}
		row.timer.Stop()
		message["id"] = row.original
		encoded, _ := json.Marshal(message)
		row.client.send(websocket.TextMessage, encoded)
		return
	}
	if len(message["method"]) == 0 {
		b.count("upstream_without_method")
		keys := make([]string, 0, len(message))
		for k := range message {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if b.count("shape:"+strings.Join(keys, ",")) == 1 {
			b.log("bridge.unknown_upstream", map[string]any{
				"keys":         keys,
				"bytes":        len(raw),
				"has_console":  bytes.Contains(raw, []byte("consoleAPICalled")),
				"inner_fields": meta.Inner,
				"outer_fields": meta.Outer,
				"frame_bytes":  meta.Bytes,
			})
		}
		return
	}
	var method, sessionID string
	json.Unmarshal(message["method"], &method)
	json.Unmarshal(message["sessionId"], &sessionID)
	b.count("event:" + method)
	b.mu.Lock()
	if b.activeTarget != source {
		b.mu.Unlock()
		b.count("stale_event")
		b.log("bridge.stale_event", map[string]any{"method": method, "session_id": sessionID})
		return
	}
	clients := []*peer{}
	for p := range b.clients {
		clients = append(clients, p)
	}
	b.mu.Unlock()
	b.log("bridge.event", map[string]any{"method": method, "session_id": sessionID, "clients": len(clients)})
	b.count("broadcast")
	for _, p := range clients {
		p.send(websocket.TextMessage, raw)
	}
}
