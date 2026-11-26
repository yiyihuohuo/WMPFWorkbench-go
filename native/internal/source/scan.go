// Package source inventories plaintext materials and reports static candidates.
package source

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"wmpfworkbench/internal/app"
)

type Item struct {
	Source string `json:"source"`
	Rel    string `json:"rel"`
	Bytes  int64  `json:"bytes"`
	Path   string `json:"path"`
}
type Materials struct {
	AppID          string        `json:"appid"`
	PlaintextFiles int           `json:"plaintext_files"`
	Items          []Item        `json:"items"`
	BaseLibrary    []app.Library `json:"base_library"`
}

func Collect(ctx context.Context, p app.Paths, id string) (Materials, error) {
	m := Materials{AppID: id, Items: []Item{}, BaseLibrary: []app.Library{}}
	if !app.AppID.MatchString(id) {
		return m, errors.New("invalid appid")
	}
	dirs, err := p.Applets()
	if err != nil {
		return m, err
	}
	for _, d := range dirs {
		base := filepath.Join(d.Path, "local", id)
		err := filepath.WalkDir(base, func(path string, e fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !e.Type().IsRegular() {
				return nil
			}
			st, err := e.Info()
			if err != nil || st.Size() == 0 || st.Size() > 8<<20 {
				return nil
			}
			head, err := app.Head(path, 2048)
			if err != nil || !app.Textish(head) {
				return nil
			}
			rel, _ := filepath.Rel(base, path)
			m.Items = append(m.Items, Item{d.Source(), filepath.ToSlash(rel), st.Size(), path})
			return nil
		})
		if err != nil {
			return m, err
		}
	}
	m.PlaintextFiles = len(m.Items)
	m.BaseLibrary, err = app.Libraries(ctx, p)
	return m, err
}

var urls = regexp.MustCompile(`https?://[a-zA-Z0-9\-._~:/?#\[\]@!$&'*+,;=%]{6,200}`)
var cloud = regexp.MustCompile(`(?s)callFunction\s*\(\s*\{[^{}]{0,200}?name\s*:\s*["']([^"'\\]{1,80})["']`)
var weakCloud = regexp.MustCompile(`(?s)["']name["']\s*:\s*["']([a-zA-Z][\w\-]{2,60})["'][^{}]{0,80}(?:data|success)`)
var wxRequest = regexp.MustCompile(`(?s)wx\.(request|uploadFile|downloadFile|connectSocket)\s*\(\s*\{[^{}]{0,300}?url\s*:\s*["']([^'"]{4,200})["']`)
var related = regexp.MustCompile(`\bwx[0-9a-f]{16}\b`)
var env = regexp.MustCompile(`environment\s*:\s*["']([^"']{3,60})["']`)

type candidate struct {
	count     int
	sources   map[string]bool
	api, note string
}

func API(ctx context.Context, p app.Paths, id string, limit, offset int) (map[string]any, error) {
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, errors.New("limit 应为 1–200，offset 应 ≥ 0")
	}
	materials, err := Collect(ctx, p, id)
	if err != nil {
		return nil, err
	}
	// decrypt_wxapkg's restore output is the primary readable source for an
	// appid whose runtime cache only holds encrypted packages.
	restoreRoot := filepath.Join(p.Root, "out", "wxapkg", id)
	err = filepath.WalkDir(restoreRoot, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || ctx.Err() != nil {
			return ctx.Err()
		}
		if !e.Type().IsRegular() {
			return nil
		}
		st, err := e.Info()
		if err != nil || st.Size() == 0 || st.Size() > 8<<20 {
			return nil
		}
		head, err := app.Head(path, 2048)
		if err != nil || !app.Textish(head) {
			return nil
		}
		rel, _ := filepath.Rel(restoreRoot, path)
		materials.Items = append(materials.Items, Item{"decrypted", filepath.ToSlash(rel), st.Size(), path})
		return nil
	})
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, i := range materials.Items {
		paths = append(paths, i.Path)
	}
	for _, l := range materials.BaseLibrary {
		if l.Plaintext {
			paths = append(paths, l.Path)
		}
	}
	u, c, w := map[string]*candidate{}, map[string]*candidate{}, map[string]*candidate{}
	envs, ids := map[string]bool{}, map[string]bool{}
	add := func(m map[string]*candidate, key, file, api, note string) {
		r := m[key]
		if r == nil {
			r = &candidate{sources: map[string]bool{}, api: api, note: note}
			m[key] = r
		}
		r.count++
		r.sources[file] = true
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, 3<<20))
		f.Close()
		if err != nil {
			continue
		}
		text := string(raw)
		file := filepath.Base(path)
		for _, v := range urls.FindAllString(text, -1) {
			v = strings.TrimRight(v, "'\",;)\\")
			key, _, _ := strings.Cut(v, "?")
			add(u, key, file, "", "")
		}
		for _, m := range cloud.FindAllStringSubmatch(text, -1) {
			add(c, m[1], file, "", "")
		}
		for _, m := range weakCloud.FindAllStringSubmatch(text, -1) {
			add(c, m[1], file, "", "weaker pattern")
		}
		for _, m := range wxRequest.FindAllStringSubmatch(text, -1) {
			add(w, m[2], "", m[1], "")
		}
		for _, m := range env.FindAllStringSubmatch(text, -1) {
			envs[m[1]] = true
		}
		for _, m := range related.FindAllString(text, -1) {
			ids[m] = true
		}
	}
	pack := func(m map[string]*candidate, skip, n int) []any {
		keys := []string{}
		for k := range m {
			keys = append(keys, k)
		}
		sort.SliceStable(keys, func(i, j int) bool {
			if m[keys[i]].count != m[keys[j]].count {
				return m[keys[i]].count > m[keys[j]].count
			}
			return keys[i] < keys[j]
		})
		out := []any{}
		for _, k := range keys[min(skip, len(keys)):min(skip+n, len(keys))] {
			v := m[k]
			sources := []string{}
			for s := range v.sources {
				if s != "" {
					sources = append(sources, s)
				}
			}
			slices.Sort(sources)
			row := map[string]any{"count": v.count, "sources": sources[:min(len(sources), 8)]}
			if v.api != "" {
				row["api"] = v.api
			}
			if v.note != "" {
				row["note"] = v.note
			}
			out = append(out, []any{k, row})
		}
		return out
	}
	sorted := func(m map[string]bool) []string {
		out := []string{}
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	relatedIDs := sorted(ids)
	return map[string]any{"appid": id, "scanned_files": len(paths), "request_urls_total": len(u), "wx_api_urls_total": len(w), "cloud_functions_total": len(c), "request_urls": pack(u, offset, limit), "wx_api_urls": pack(w, 0, min(limit, 80)), "cloud_functions": pack(c, 0, min(limit, 80)), "cloud_env": sorted(envs), "related_appids": relatedIDs[:min(len(relatedIDs), 40)], "limit": limit, "offset": offset, "grade_note": "静态命中一律 candidate：源码里出现 URL / callFunction 名不等于接口可达；confirmed 需 NetLog 或 CDP Network 运行期证据"}, nil
}
