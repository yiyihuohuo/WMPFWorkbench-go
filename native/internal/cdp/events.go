package cdp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

var ErrCursor = errors.New("cursor_invalid: malformed, expired connection, or future cursor")
var filterPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*\.([A-Za-z][A-Za-z0-9]*|\*)$`)

// ASCIIJSON matches Python's conservative ensure_ascii=True budget, including separators.
func ASCIIJSON(v any) ([]byte, error) {
	var compact bytes.Buffer
	enc := json.NewEncoder(&compact)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	quoted, escape := false, false
	for _, r := range strings.TrimSuffix(compact.String(), "\n") {
		if r > 127 {
			if r <= 0xffff {
				fmt.Fprintf(&out, `\u%04x`, r)
			} else {
				a, b := utf16.EncodeRune(r)
				fmt.Fprintf(&out, `\u%04x\u%04x`, a, b)
			}
			continue
		}
		out.WriteByte(byte(r))
		if escape {
			escape = false
			continue
		}
		if r == '\\' && quoted {
			escape = true
			continue
		}
		if r == '"' {
			quoted = !quoted
		}
		if !quoted && (r == ':' || r == ',') {
			out.WriteByte(' ')
		}
	}
	return out.Bytes(), nil
}
func cloned(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return out
}

type eventRecord struct {
	seq    uint64
	method string
	data   []byte
}
type scriptRecord struct {
	key  string
	data []byte
}
type Buffer struct {
	mu                                             sync.Mutex
	connection                                     string
	seq                                            uint64
	rows                                           []eventRecord
	bytes, maxEvents, maxBytes, dropped, truncated int
	scripts                                        []scriptRecord
	scriptBytes, scriptDropped                     int
	closed                                         bool
	changed                                        chan struct{}
}
type ReadOptions struct {
	Methods  []string
	Limit    int
	Since    string
	WaitMS   int
	MaxBytes int
}
type BufferStats struct {
	Events    int `json:"events"`
	Bytes     int `json:"bytes"`
	Dropped   int `json:"dropped_events"`
	Truncated int `json:"truncated_events"`
	MaxEvents int `json:"max_events"`
	MaxBytes  int `json:"max_bytes"`
}
type Page struct {
	OK              bool             `json:"ok"`
	Events          []map[string]any `json:"events"`
	Count           int              `json:"count"`
	NextCursor      string           `json:"next_cursor"`
	HasMore         bool             `json:"has_more"`
	Gap             bool             `json:"gap"`
	MissedEvents    uint64           `json:"missed_events"`
	ConnectionState string           `json:"connection_state"`
	OldestCursor    string           `json:"oldest_cursor"`
	LatestCursor    string           `json:"latest_cursor"`
	Buffer          BufferStats      `json:"buffer"`
}

func NewBuffer(maxEvents, maxBytes int) *Buffer {
	if maxEvents < 1 {
		maxEvents = 4096
	}
	if maxBytes < 2048 {
		maxBytes = 8 << 20
	}
	var id [16]byte
	rand.Read(id[:])
	return &Buffer{connection: hex.EncodeToString(id[:]), maxEvents: maxEvents, maxBytes: maxBytes, changed: make(chan struct{})}
}
func (b *Buffer) cursor(seq uint64) string { return b.connection + ":" + strconv.FormatUint(seq, 10) }
func (b *Buffer) notify()                  { close(b.changed); b.changed = make(chan struct{}) }
func preview(m map[string]any, original int) map[string]any {
	raw, _ := ASCIIJSON(m)
	if len(raw) > 1024 {
		raw = raw[:1024]
	}
	p := map[string]any{"seq": m["seq"], "received_at": m["received_at"], "method": m["method"], "params": map[string]any{}, "truncated": true, "original_bytes": original, "preview_json": string(raw)}
	return p
}
func shrink(m map[string]any) { s, _ := m["preview_json"].(string); m["preview_json"] = s[:len(s)/2] }
func (b *Buffer) Append(message map[string]any) {
	method, ok := message["method"].(string)
	if !ok || len(method) > 128 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if method == "Debugger.scriptParsed" {
		p, _ := message["params"].(map[string]any)
		if inner, ok := p["script"].(map[string]any); ok {
			p = inner
		}
		if id, ok := p["scriptId"]; ok {
			key := fmt.Sprint(message["sessionId"], "/", id)
			raw, _ := ASCIIJSON(p)
			for i, row := range b.scripts {
				if row.key == key {
					b.scriptBytes -= len(row.data)
					b.scripts = append(b.scripts[:i], b.scripts[i+1:]...)
					break
				}
			}
			if len(raw) <= 4<<20 {
				b.scripts = append(b.scripts, scriptRecord{key, raw})
				b.scriptBytes += len(raw)
			} else {
				b.scriptDropped++
			}
			for len(b.scripts) > 4096 || b.scriptBytes > 4<<20 {
				b.scriptBytes -= len(b.scripts[0].data)
				b.scripts[0] = scriptRecord{}
				b.scripts = b.scripts[1:]
				b.scriptDropped++
			}
		}
	}
	b.seq++
	m := cloned(message)
	m["seq"] = b.seq
	m["received_at"] = float64(time.Now().UnixNano()) / 1e9
	raw, _ := ASCIIJSON(m)
	budget := min(16384, b.maxBytes)
	if len(raw) > budget {
		m = preview(m, len(raw))
		b.truncated++
		raw, _ = ASCIIJSON(m)
		for len(raw) > budget {
			shrink(m)
			raw, _ = ASCIIJSON(m)
		}
	}
	b.rows = append(b.rows, eventRecord{b.seq, method, raw})
	b.bytes += len(raw)
	for len(b.rows) > b.maxEvents || b.bytes > b.maxBytes {
		b.bytes -= len(b.rows[0].data)
		b.rows[0] = eventRecord{}
		b.rows = b.rows[1:]
		b.dropped++
	}
	b.notify()
}
func (b *Buffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.notify()
	}
}
func (b *Buffer) Scripts() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []map[string]any{}
	for _, r := range b.scripts {
		var m map[string]any
		json.Unmarshal(r.data, &m)
		out = append(out, m)
	}
	return out
}
func (b *Buffer) ScriptStatus() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{"entries": len(b.scripts), "bytes": b.scriptBytes, "dropped_entries": b.scriptDropped, "max_entries": 4096, "max_bytes": 4 << 20}
}
func (b *Buffer) position(cursor string) (uint64, error) {
	if cursor == "" {
		if len(b.rows) > 0 {
			return b.rows[0].seq - 1, nil
		}
		return b.seq, nil
	}
	parts := strings.Split(cursor, ":")
	if len(cursor) > 64 || len(parts) != 2 || parts[0] != b.connection {
		return 0, ErrCursor
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || seq > b.seq {
		return 0, ErrCursor
	}
	return seq, nil
}
func matches(method string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if strings.HasSuffix(f, ".*") {
			if strings.HasPrefix(method, f[:len(f)-1]) {
				return true
			}
		} else if method == f {
			return true
		}
	}
	return false
}
func (b *Buffer) page(position uint64, opts ReadOptions) Page {
	oldest := b.seq + 1
	if len(b.rows) > 0 {
		oldest = b.rows[0].seq
	}
	scanned := max(position, oldest-1)
	p := Page{OK: true, Events: []map[string]any{}, NextCursor: b.cursor(scanned), OldestCursor: b.cursor(oldest - 1), LatestCursor: b.cursor(b.seq), ConnectionState: "connected", Buffer: BufferStats{len(b.rows), b.bytes, b.dropped, b.truncated, b.maxEvents, b.maxBytes}}
	if b.closed {
		p.ConnectionState = "closed"
	}
	if position < oldest-1 {
		p.Gap = true
		p.MissedEvents = oldest - 1 - position
	}
	if opts.Since == "now" {
		return p
	}
	for _, r := range b.rows {
		if r.seq <= position {
			continue
		}
		if !matches(r.method, opts.Methods) {
			scanned = r.seq
			continue
		}
		if len(p.Events) == opts.Limit {
			p.HasMore = true
			break
		}
		var m map[string]any
		json.Unmarshal(r.data, &m)
		p.Events = append(p.Events, m)
		p.Count = len(p.Events)
		p.NextCursor = b.cursor(r.seq)
		encoded, _ := ASCIIJSON(p)
		if len(encoded) > opts.MaxBytes {
			if len(p.Events) > 1 {
				p.Events = p.Events[:len(p.Events)-1]
				p.HasMore = true
				break
			}
			m = preview(m, len(r.data))
			p.Events[0] = m
			encoded, _ = ASCIIJSON(p)
			for len(encoded) > opts.MaxBytes {
				shrink(m)
				encoded, _ = ASCIIJSON(p)
			}
		}
		scanned = r.seq
	}
	p.Count = len(p.Events)
	p.NextCursor = b.cursor(scanned)
	return p
}
func (b *Buffer) Read(ctx context.Context, opts ReadOptions) (Page, error) {
	if opts.Limit == 0 {
		opts.Limit = 50
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 32000
	}
	if opts.Limit < 1 || opts.Limit > 200 || opts.MaxBytes < 2048 || opts.MaxBytes > 40000 || opts.WaitMS < 0 || opts.WaitMS > 3000 || len(opts.Methods) > 32 {
		return Page{}, errors.New("invalid event read limits")
	}
	for _, f := range opts.Methods {
		if len(f) > 128 || !filterPattern.MatchString(f) {
			return Page{}, errors.New("method_filter requires exact method names or Domain.*")
		}
	}
	deadline := time.NewTimer(time.Duration(opts.WaitMS) * time.Millisecond)
	defer deadline.Stop()
	b.mu.Lock()
	position, err := b.position(opts.Since)
	if opts.Since == "now" {
		position = b.seq
		err = nil
	}
	b.mu.Unlock()
	if err != nil {
		return Page{}, err
	}
	for {
		b.mu.Lock()
		p := b.page(position, opts)
		changed := b.changed
		closed := b.closed
		b.mu.Unlock()
		if len(p.Events) > 0 || p.Gap || closed || opts.Since == "now" || opts.WaitMS == 0 {
			return p, nil
		}
		select {
		case <-ctx.Done():
			return Page{}, ctx.Err()
		case <-deadline.C:
			b.mu.Lock()
			p = b.page(position, opts)
			b.mu.Unlock()
			return p, nil
		case <-changed:
		}
	}
}
