// Package app owns installation paths and read-only runtime-profile discovery.
package app

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var AppID = regexp.MustCompile(`^wx[0-9a-f]{16}$`)
var userID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var cacheName = regexp.MustCompile(`^([0-9a-f]{32})-(\d+)-(.+)\.cachedata$`)

type Profile struct{ Label, Path string }
type Applet struct{ Label, UID, Path string }

func (a Applet) Source() string { return a.Label + "/" + a.UID[:12] }

type Paths struct {
	Root, Out, Data, Profile, Runtime string
	Roots                             []Profile
}

func Resolve(root string) (Paths, error) {
	if root == "" {
		root = os.Getenv("WMPF_HOME")
	}
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			return Paths{}, err
		}
		root = filepath.Dir(exe)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, err
	}
	env := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}
	p := Paths{Root: root, Out: filepath.Join(root, "out"), Data: filepath.Join(root, "data"), Profile: env("WMPF_DATA_ROOT", filepath.Join(root, "data", "runtime-profile")), Runtime: env("WMPF_RUNTIME_DIR", filepath.Join(root, "runtime", "5.10.7300.414"))}
	p.Roots = []Profile{{"workbench", p.Profile}}
	if roaming := os.Getenv("APPDATA"); roaming != "" {
		p.Roots = append(p.Roots, Profile{"wechat4x", filepath.Join(roaming, "Tencent", "xwechat", "radium")})
	}
	return p, nil
}
func (p Paths) Applets() ([]Applet, error) {
	out := []Applet{}
	for _, r := range p.Roots {
		entries, err := os.ReadDir(filepath.Join(r.Path, "users"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || !userID.MatchString(e.Name()) {
				continue
			}
			path := filepath.Join(r.Path, "users", e.Name(), "applet")
			if st, err := os.Stat(path); err == nil && st.IsDir() {
				out = append(out, Applet{r.Label, e.Name(), path})
			}
		}
	}
	return out, nil
}
func (p Paths) Describe() map[string]any {
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	dirs, _ := p.Applets()
	sources := [][2]string{}
	for _, d := range dirs {
		sources = append(sources, [2]string{d.Label, d.UID})
	}
	return map[string]any{"root": p.Root, "runtime_dir": p.Runtime, "runtime_exists": exists(p.Runtime), "ams_dll": exists(filepath.Join(p.Runtime, "runtime", "libamssdk.dll")), "pcyyb_dll": exists(filepath.Join(p.Runtime, "runtime", "pcyyb_sdk_dll.dll")), "host_export_x64": exists(filepath.Join(p.Runtime, "host", "wmpf_host_export_x64.dll")), "exe": exists(filepath.Join(p.Runtime, "runtime", "WeChatAppEx.exe")), "user_applet_dirs": sources}
}

type Package struct {
	AppID     string `json:"appid"`
	Version   string `json:"version"`
	File      string `json:"file"`
	Bytes     int64  `json:"bytes"`
	Magic     string `json:"magic"`
	Encrypted bool   `json:"encrypted"`
	Source    string `json:"source"`
	Path      string `json:"path"`
}

func Head(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, n)
	count, err := io.ReadFull(f, b)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return b[:count], err
}
func ListPackages(ctx context.Context, p Paths, id string) ([]Package, error) {
	if !AppID.MatchString(id) {
		return nil, errors.New("appid 必须是 wx+16位小写十六进制")
	}
	dirs, err := p.Applets()
	if err != nil {
		return nil, err
	}
	out := []Package{}
	for _, d := range dirs {
		base := filepath.Join(d.Path, "packages", id)
		versions, err := os.ReadDir(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			files, err := os.ReadDir(filepath.Join(base, v.Name()))
			if err != nil {
				return nil, err
			}
			for _, f := range files {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if !f.Type().IsRegular() || !strings.HasSuffix(f.Name(), ".wxapkg") {
					continue
				}
				path := filepath.Join(base, v.Name(), f.Name())
				st, err := f.Info()
				if err != nil {
					return nil, err
				}
				head, err := Head(path, 6)
				if err != nil {
					return nil, err
				}
				runes := make([]rune, len(head))
				for i, b := range head {
					runes[i] = rune(b)
				}
				out = append(out, Package{id, v.Name(), f.Name(), st.Size(), string(runes), string(head) == "V1MMWX", d.Source(), path})
			}
		}
	}
	return out, nil
}

type Library struct {
	MD5       string `json:"md5"`
	Version   string `json:"version"`
	RealName  string `json:"real_name"`
	Source    string `json:"source"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Plaintext bool   `json:"plaintext"`
}

func Textish(blob []byte) bool {
	if len(blob) == 0 {
		return false
	}
	for _, magic := range []string{"\x89PNG", "\xff\xd8", "RIFF", "GIF8", "\x00\x00\x01\x00"} {
		if strings.HasPrefix(string(blob), magic) {
			return false
		}
	}
	blob = blob[:min(len(blob), 4096)]
	good := 0
	for _, b := range blob {
		if b >= 32 && b < 127 || b == 9 || b == 10 || b == 13 {
			good++
		}
	}
	return float64(good)/float64(len(blob)) > .85
}
func Libraries(ctx context.Context, p Paths) ([]Library, error) {
	dirs, err := p.Applets()
	if err != nil {
		return nil, err
	}
	out := []Library{}
	for _, d := range dirs {
		base := filepath.Join(d.Path, "codecache")
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
			m := cacheName.FindStringSubmatch(e.Name())
			if m == nil {
				return nil
			}
			name := m[3]
			if raw, err := base64.StdEncoding.DecodeString(name + strings.Repeat("=", (4-len(name)%4)%4)); err == nil {
				name = strings.ToValidUTF8(string(raw), "�")
			}
			st, err := e.Info()
			if err != nil {
				return err
			}
			head, err := Head(path, 256)
			if err != nil {
				return err
			}
			out = append(out, Library{m[1], m[2], name, d.Source(), path, st.Size(), Textish(head)})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
