package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"sync"
)

type response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}
type Session struct {
	ws        *websocket.Conn
	mu, write sync.Mutex
	id        int64
	pending   map[int64]chan response
	done      chan struct{}
	once      sync.Once
	Buffer    *Buffer
}

func Dial(ctx context.Context, url string) (*Session, error) {
	dialer := websocket.Dialer{Proxy: nil}
	ws, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(64 << 20)
	s := &Session{ws: ws, pending: map[int64]chan response{}, done: make(chan struct{}), Buffer: NewBuffer(4096, 8<<20)}
	go s.pump()
	return s, nil
}
func (s *Session) pump() {
	defer s.Close()
	for {
		_, raw, err := s.ws.ReadMessage()
		if err != nil {
			return
		}
		var header map[string]json.RawMessage
		if json.Unmarshal(raw, &header) != nil {
			continue
		}
		if header["id"] != nil {
			var r response
			if json.Unmarshal(raw, &r) != nil {
				continue
			}
			s.mu.Lock()
			q := s.pending[r.ID]
			s.mu.Unlock()
			if q != nil {
				select {
				case q <- r:
				default:
				}
			}
		} else if header["method"] != nil {
			var e map[string]any
			if json.Unmarshal(raw, &e) == nil {
				s.Buffer.Append(e)
			}
		}
	}
}
func (s *Session) Close() { s.once.Do(func() { close(s.done); s.Buffer.Close(); s.ws.Close() }) }
func (s *Session) Send(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, error) {
	select {
	case <-s.done:
		return nil, errors.New("CDP connection closed")
	default:
	}
	s.mu.Lock()
	s.id++
	id := s.id
	q := make(chan response, 1)
	s.pending[id] = q
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	if params == nil {
		params = map[string]any{}
	}
	message := map[string]any{"id": id, "method": method, "params": params}
	if sessionID != "" {
		message["sessionId"] = sessionID
	}
	s.write.Lock()
	err := s.ws.WriteJSON(message)
	s.write.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errors.New("CDP connection closed")
	case r := <-q:
		if len(r.Error) > 0 && string(r.Error) != "null" {
			return nil, fmt.Errorf("%s: %s", method, r.Error)
		}
		return r.Result, nil
	}
}
