package source

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"wmpfworkbench/internal/app"
)

func TestScanPlaintextAndSharedLibraries(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	p.Roots = []app.Profile{{Label: "fixture", Path: p.Profile}}
	base := filepath.Join(p.Profile, "users", "0123456789abcdef0123456789abcdef", "applet")
	id := "wx0123456789abcdef"
	write := func(path, data string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(base, "local", id, "app.js"), `wx.request({url:"https://example.test/api?token=private"}); wx.cloud.callFunction({name:"hello", data:{}}); var x={environment:"env-demo"};`)
	write(filepath.Join(base, "local", id, "binary"), "\x89PNG\x00\x01\x02")
	name := base64.StdEncoding.EncodeToString([]byte("WAPCAdapter.js"))
	write(filepath.Join(base, "codecache", "1", "0123456789abcdef0123456789abcdef-1-"+name+".cachedata"), `var endpoint="https://library.test/resource";`)
	files, err := Collect(context.Background(), p, id)
	if err != nil || len(files.Items) != 1 || len(files.BaseLibrary) != 1 || files.BaseLibrary[0].RealName != "WAPCAdapter.js" {
		t.Fatalf("collect: %+v %v", files, err)
	}
	result, err := API(context.Background(), p, id, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result["request_urls_total"] != 2 || result["cloud_functions_total"] != 1 {
		t.Fatalf("api: %+v", result)
	}
	raw := result["request_urls"].([]any)
	if len(raw) != 1 {
		t.Fatalf("pagination: %+v", raw)
	}
}

// decrypt_wxapkg's restore output is the primary readable source for an appid
// whose runtime cache only holds encrypted packages; API() must scan it.
func TestAPIScansDecryptedRestoreOutput(t *testing.T) {
	p, err := app.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "wx0123456789abcdef"
	dir := filepath.Join(p.Root, "out", "wxapkg", id, "1594", "workbench_uid-task")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	code := `wx.request({ url: "https://api.example.com/v1/order" })`
	if err := os.WriteFile(filepath.Join(dir, "index.appservice.js"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := API(context.Background(), p, id, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result["request_urls_total"].(int) < 1 {
		t.Fatal("restore output not scanned", result)
	}
	if result["scanned_files"].(int) < 1 {
		t.Fatal("no files scanned", result)
	}
}
