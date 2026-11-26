// Package netlog parses Chromium exports, including interrupted JSON streams.
package netlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type document struct {
	Constants map[string]any   `json:"constants"`
	Events    []map[string]any `json:"events"`
}

func decode(raw []byte) (document, bool) {
	var doc document
	if json.Unmarshal(raw, &doc) == nil {
		return doc, false
	}
	doc = document{Constants: map[string]any{}, Events: []map[string]any{}}
	// Decode only complete values. Never repair a partial event by inventing fields.
	if i := bytes.Index(raw, []byte(`"constants"`)); i >= 0 {
		if j := bytes.IndexByte(raw[i:], '{'); j >= 0 {
			json.NewDecoder(bytes.NewReader(raw[i+j:])).Decode(&doc.Constants)
		}
	}
	if i := bytes.Index(raw, []byte(`"events"`)); i >= 0 {
		if j := bytes.IndexByte(raw[i:], '['); j >= 0 {
			pos := i + j + 1
			for pos < len(raw) {
				for pos < len(raw) && strings.ContainsRune(" \t\r\n,", rune(raw[pos])) {
					pos++
				}
				if pos >= len(raw) || raw[pos] != '{' {
					break
				}
				d := json.NewDecoder(bytes.NewReader(raw[pos:]))
				var ev map[string]any
				if d.Decode(&ev) != nil {
					break
				}
				doc.Events = append(doc.Events, ev)
				pos += int(d.InputOffset())
			}
		}
	}
	return doc, true
}
func enum(constants map[string]any, key, alias string, value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	n, ok := value.(float64)
	if !ok {
		return fmt.Sprint(value)
	}
	for _, k := range []string{key, alias} {
		switch v := constants[k].(type) {
		case []any:
			if n >= 0 && n < float64(len(v)) {
				if s, ok := v[int(n)].(string); ok {
					return s
				}
			}
		case map[string]any:
			for name, id := range v {
				if id == n {
					return name
				}
			}
		}
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

var method = regexp.MustCompile(`"?method"?\s*[:=]\s*"?([A-Z]+)"?`)
var envID = regexp.MustCompile(`https?://([a-z0-9-]{8,60})\.`)
var cloudPatterns = [][2]string{{`\.tcb-api\.tencentcloudapi\.com`, "tcb-api"}, {`\.tcloudbaseapp\.com`, "tcloudbase"}, {`\.tcb\.qcloud\.la`, "tcb-qcloud"}, {`cloudfunctions`, "cloudfunctions"}, {`\.ap-[a-z]+\.tencentcloudbase\.cn`, "tencentcloudbase"}, {`tencentcloudapi\.com`, "tencentcloudapi"}, {`\.servicewechat\.com`, "wxa-resource"}, {`api\.weixin\.qq\.com`, "wx-api"}}

func cleanURL(s string) string {
	return strings.TrimSpace(s)
}
func Analyze(path string) (map[string]any, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("文件不存在或不是普通文件")
	}
	// The parser has a deliberate ceiling rather than allocating unbounded data
	// from arbitrary exports; streaming support can raise this in a later version.
	if st.Size() > 256<<20 {
		return nil, errors.New("NetLog 超过 256 MiB 解析上限")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, repaired := decode(raw)
	summary := map[string]any{"path": path, "bytes": st.Size(), "events": len(doc.Events), "truncated_salvage": repaired, "event_kinds": map[string]int{}, "constants_keys": []string{}, "first_time": nil, "last_time": nil}
	api := map[string]any{"path": path, "bytes": st.Size(), "total_url_events": 0, "distinct_paths": 0, "hosts": map[string]any{}, "methods": map[string]int{}, "status_codes": map[string]int{}, "cloud_candidates": []any{}, "cloud_env_ids": []string{}}
	if st.Size() == 0 {
		summary = map[string]any{"path": path, "empty": true}
		api = map[string]any{"path": path, "empty": true, "hosts": map[string]any{}, "cloud_candidates": []any{}}
		return map[string]any{"ok": true, "action": "analyze", "path": path, "summary": summary, "api_surface": api}, nil
	}
	kinds, paths, hosts, methods, statuses := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	samples := map[string][]string{}
	hostPaths := map[string]map[string]bool{}
	envs := map[string]bool{}
	for _, ev := range doc.Events {
		name := enum(doc.Constants, "event_type", "logEventTypes", ev["type"])
		kinds[strings.Split(name, ".")[0]]++
		p, _ := ev["params"].(map[string]any)
		if len(p) == 0 {
			p, _ = ev["extra"].(map[string]any)
		}
		u := ""
		for _, key := range []string{"url", "request_url", "action", "resource_url", "base_url"} {
			if s, ok := p[key].(string); ok && (strings.HasPrefix(s, "http") || strings.HasPrefix(s, "ws")) {
				u = s
				break
			}
		}
		if u == "" {
			for _, v := range p {
				if s, ok := v.(string); ok && strings.HasPrefix(s, "http") && len(s) > 12 {
					u = s
					break
				}
			}
		}
		if u != "" {
			u = cleanURL(u)
			base, _, _ := strings.Cut(u, "?")
			paths[base]++
			parsed, _ := url.Parse(u)
			if parsed != nil && parsed.Hostname() != "" {
				h := parsed.Hostname()
				hosts[h]++
				if len(samples[h]) < 3 {
					r := []rune(u)
					samples[h] = append(samples[h], string(r[:min(len(r), 220)]))
				}
				if hostPaths[h] == nil {
					hostPaths[h] = map[string]bool{}
				}
				hostPaths[h][base] = true
			}
			encoded, _ := json.Marshal(p)
			if m := method.FindSubmatch(encoded[:min(len(encoded), 400)]); m != nil {
				methods[string(m[1])]++
			}
			if m := envID.FindStringSubmatch(base); m != nil {
				envs[m[1]] = true
			}
		}
		for _, key := range []string{"http_status_code", "status_code"} {
			if v := p[key]; v != nil && v != float64(0) && v != "" {
				statuses[fmt.Sprint(v)]++
				break
			}
		}
	}
	keys := []string{}
	for k := range doc.Constants {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	summary["constants_keys"] = keys
	kindNames := []string{}
	for k := range kinds {
		kindNames = append(kindNames, k)
	}
	sort.Slice(kindNames, func(i, j int) bool {
		if kinds[kindNames[i]] != kinds[kindNames[j]] {
			return kinds[kindNames[i]] > kinds[kindNames[j]]
		}
		return kindNames[i] < kindNames[j]
	})
	top := map[string]int{}
	for _, k := range kindNames[:min(len(kindNames), 20)] {
		top[k] = kinds[k]
	}
	summary["event_kinds"] = top
	if len(doc.Events) > 0 {
		summary["first_time"] = doc.Events[0]["time"]
		summary["last_time"] = doc.Events[len(doc.Events)-1]["time"]
	}
	perHost := map[string]any{}
	for h, n := range hosts {
		list := []string{}
		for path := range hostPaths[h] {
			list = append(list, path)
		}
		slices.Sort(list)
		perHost[h] = map[string]any{"requests": n, "distinct_urls": list[:min(len(list), 40)], "samples": samples[h]}
	}
	clouds := []any{}
	for _, spec := range cloudPatterns {
		rx := regexp.MustCompile(spec[0])
		hits := []string{}
		for path := range paths {
			if rx.MatchString(path) {
				hits = append(hits, path)
			}
		}
		slices.Sort(hits)
		if len(hits) > 0 {
			clouds = append(clouds, map[string]any{"tag": spec[1], "pattern": spec[0], "count": len(hits), "urls": hits[:min(len(hits), 20)]})
		}
	}
	total := 0
	for _, n := range paths {
		total += n
	}
	ids := []string{}
	for id := range envs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	api["total_url_events"] = total
	api["distinct_paths"] = len(paths)
	api["hosts"] = perHost
	api["methods"] = methods
	api["status_codes"] = statuses
	api["cloud_candidates"] = clouds
	api["cloud_env_ids"] = ids[:min(len(ids), 40)]
	return map[string]any{"ok": true, "action": "analyze", "path": path, "summary": summary, "api_surface": api}, nil
}
func Discover(roots []string) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(root, "log", "net-export"), func(path string, e fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !e.Type().IsRegular() {
				return nil
			}
			st, err := e.Info()
			if err != nil {
				return err
			}
			out = append(out, map[string]any{"path": path, "bytes": st.Size()})
			return nil
		})
		if err != nil {
			return nil, err
		}
		alt := filepath.Join(root, "temp-xweb-net-export")
		if st, err := os.Stat(alt); err == nil && st.IsDir() {
			out = append(out, map[string]any{"path": alt, "bytes": 0, "dir_only": true})
		}
	}
	return out, nil
}
func Trigger() map[string]any {
	return map[string]any{"start": map[string]any{"method": "XWeb.NetLogStart", "params": map[string]any{"path": "<wmpf_root>/log/net-export", "captureMode": "Default", "categories": []string{"*"}, "recordExperiments": false}}, "stop": map[string]any{"method": "XWeb.NetLogStop", "params": map[string]any{}}, "status": map[string]any{"method": "XWeb.NetLogStatus", "params": map[string]any{}}, "path": map[string]any{"method": "XWeb.NetLogPath", "params": map[string]any{}}, "path_all": map[string]any{"method": "XWeb.NetLogPathAll", "params": map[string]any{}}, "delete": map[string]any{"method": "XWeb.NetLogDelete", "params": map[string]any{}}, "prereq": "先经 cdp_connect 连上自产宿主本地调试桥 ws://127.0.0.1:62000，再由 cdp_send 发送 XWeb.NetLogStart；该命令在本运行时能否落盘尚未实测，用 netlog(discover) 核查结果", "fallback_cli": "WeChatAppEx.exe --log-net-log=<file> --net-log-capture-mode=Default"}
}
