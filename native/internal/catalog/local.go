package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"wmpfworkbench/internal/app"
)

type IndexedApp struct {
	AppID           string         `json:"appid"`
	Name            *string        `json:"name"`
	NameSource      string         `json:"name_source,omitempty"`
	Sources         []string       `json:"sources"`
	Packages        []app.Package  `json:"packages"`
	Versions        []string       `json:"versions"`
	SubpackageFiles []string       `json:"subpackage_files"`
	PackageCount    int            `json:"package_count"`
	PkgBytes        int64          `json:"pkg_bytes"`
	PkgEncryptedAll *bool          `json:"pkg_encrypted_all"`
	LocalFiles      int            `json:"local_files,omitempty"`
	LocalBytes      int64          `json:"local_bytes,omitempty"`
	LocalGroups     map[string]int `json:"local_groups,omitempty"`
	IconGlob        string         `json:"icon_glob,omitempty"`
}
type Index struct {
	GeneratedAt string                   `json:"generated_at"`
	Runtime     map[string]any           `json:"runtime"`
	AppCount    int                      `json:"app_count"`
	Apps        []IndexedApp             `json:"apps"`
	BaseLibs    map[string][]app.Library `json:"base_libs"`
}
type Local struct {
	Paths app.Paths
	mu    sync.Mutex
}

func NewLocal(p app.Paths) *Local { return &Local{Paths: p} }
func (s *Local) JSONPath() string { return filepath.Join(s.Paths.Out, "catalog_index.json") }
func (s *Local) DBPath() string   { return filepath.Join(s.Paths.Out, "catalog_index.db") }

var chinese = regexp.MustCompile(`[\x{4e00}-\x{9fa5}]{2,24}`)
var nameKeys = []string{"app_name", "nickname", "title", "appName", "name", "username"}

func mineName(path, id string) (string, string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() <= 16 || st.Size() > 262144 {
		return "", ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	text := string(raw)
	if !strings.Contains(text, id) && !strings.Contains(text, "app_name") {
		return "", ""
	}
	for _, key := range nameKeys {
		rx := regexp.MustCompile(`"` + key + `"\s*:\s*"([^"\r\n]{1,240})"`)
		for _, m := range rx.FindAllStringSubmatch(text, -1) {
			v := strings.TrimSpace(m[1])
			if len([]rune(v)) > 1 && len([]rune(v)) <= 60 && !strings.HasPrefix(v, "http") {
				var decoded string
				if json.Unmarshal([]byte(`"`+m[1]+`"`), &decoded) == nil {
					v = decoded
				}
				return v, "json_key:" + key + ":" + filepath.Base(path)
			}
		}
	}
	if strings.Contains(text, id) {
		best := ""
		for _, hit := range chinese.FindAllString(text, -1) {
			if len([]rune(hit)) >= 3 || strings.ContainsAny(hit, "应用小") {
				if len([]rune(hit)) > len([]rune(best)) {
					best = hit
				}
			}
		}
		if best != "" {
			return best, "cjk_near_appid:" + filepath.Base(path)
		}
	}
	return "", ""
}
func (s *Local) Rebuild(ctx context.Context) (Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuild(ctx)
}
func (s *Local) rebuild(ctx context.Context) (Index, error) {
	idx := Index{GeneratedAt: time.Now().Format("2006-01-02 15:04:05"), Runtime: s.Paths.Describe(), Apps: []IndexedApp{}, BaseLibs: map[string][]app.Library{}}
	dirs, err := s.Paths.Applets()
	if err != nil {
		return idx, err
	}
	apps := map[string]*IndexedApp{}
	for _, d := range dirs {
		ids := map[string]bool{}
		for _, kind := range []string{"packages", "local"} {
			entries, err := os.ReadDir(filepath.Join(d.Path, kind))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return idx, err
			}
			for _, e := range entries {
				if e.IsDir() && app.AppID.MatchString(e.Name()) {
					ids[e.Name()] = true
				}
			}
		}
		for id := range ids {
			if err := ctx.Err(); err != nil {
				return idx, err
			}
			a := apps[id]
			if a == nil {
				a = &IndexedApp{AppID: id, Sources: []string{}, Packages: []app.Package{}, Versions: []string{}, SubpackageFiles: []string{}, LocalGroups: map[string]int{}}
				apps[id] = a
			}
			a.Sources = append(a.Sources, d.Source())
			base := filepath.Join(d.Path, "local", id)
			candidates := 0
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
				if err != nil {
					return nil
				}
				a.LocalFiles++
				a.LocalBytes += st.Size()
				rel, _ := filepath.Rel(base, path)
				a.LocalGroups[strings.Split(filepath.ToSlash(rel), "/")[0]]++
				if a.Name == nil && candidates < 400 {
					candidates++
					name, source := mineName(path, id)
					if name != "" {
						a.Name = &name
						a.NameSource = source
					}
				}
				return nil
			})
			if err != nil {
				return idx, err
			}
			if a.Name == nil {
				a.NameSource = "not_found"
			}
			icons, _ := os.ReadDir(filepath.Join(d.Path, "icon"))
			for _, icon := range icons {
				if icon.Type().IsRegular() && strings.HasPrefix(icon.Name(), id+"_") {
					a.IconGlob = filepath.Join(d.Path, "icon", icon.Name())
					break
				}
			}
		}
	}
	for id, a := range apps {
		pk, err := app.ListPackages(ctx, s.Paths, id)
		if err != nil {
			return idx, err
		}
		a.PackageCount = len(pk)
		a.Packages = pk
		encrypted := true
		for _, r := range pk {
			a.PkgBytes += r.Bytes
			encrypted = encrypted && r.Encrypted
			if !slices.Contains(a.Versions, r.Version) {
				a.Versions = append(a.Versions, r.Version)
			}
			if !slices.Contains(a.SubpackageFiles, r.File) {
				a.SubpackageFiles = append(a.SubpackageFiles, r.File)
			}
		}
		if len(pk) > 0 {
			a.PkgEncryptedAll = &encrypted
		}
		slices.Sort(a.Versions)
		slices.Sort(a.SubpackageFiles)
		slices.Sort(a.Sources)
		a.Packages = a.Packages[:min(len(a.Packages), 60)]
		idx.Apps = append(idx.Apps, *a)
	}
	sort.Slice(idx.Apps, func(i, j int) bool {
		if idx.Apps[i].PkgBytes != idx.Apps[j].PkgBytes {
			return idx.Apps[i].PkgBytes > idx.Apps[j].PkgBytes
		}
		return idx.Apps[i].AppID < idx.Apps[j].AppID
	})
	idx.AppCount = len(idx.Apps)
	libs, err := app.Libraries(ctx, s.Paths)
	if err != nil {
		return idx, err
	}
	for _, l := range libs {
		idx.BaseLibs[l.Source] = append(idx.BaseLibs[l.Source], l)
	}
	if err = writeSQLite(ctx, s.DBPath(), idx); err != nil {
		return idx, err
	}
	err = app.WriteJSON(s.JSONPath(), idx)
	return idx, err
}
func (s *Local) Load(ctx context.Context) (Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.JSONPath())
	if errors.Is(err, os.ErrNotExist) {
		return s.rebuild(ctx)
	}
	var idx Index
	if err == nil {
		err = json.Unmarshal(raw, &idx)
	}
	return idx, err
}
func (s *Local) Search(ctx context.Context, keyword string, limit int) ([]IndexedApp, error) {
	idx, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	out := []IndexedApp{}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	for _, a := range idx.Apps {
		name := ""
		if a.Name != nil {
			name = *a.Name
		}
		hay := strings.ToLower(a.AppID + " " + name + " " + strings.Join(a.SubpackageFiles, " ") + " " + strings.Join(a.Sources, " "))
		if strings.Contains(hay, keyword) {
			out = append(out, a)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (s *Local) Get(ctx context.Context, id string) (*IndexedApp, error) {
	idx, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range idx.Apps {
		if a.AppID == id {
			return &a, nil
		}
	}
	return nil, nil
}
func (s *Local) Stats(ctx context.Context) (map[string]any, error) {
	idx, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	named, pk := 0, 0
	for _, a := range idx.Apps {
		if a.Name != nil {
			named++
		}
		if a.PackageCount > 0 {
			pk++
		}
	}
	return map[string]any{"app_count": idx.AppCount, "named": named, "with_packages": pk, "sources": idx.Runtime["user_applet_dirs"], "generated_at": idx.GeneratedAt}, nil
}
