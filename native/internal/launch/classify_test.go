package launch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"wmpfworkbench/internal/account"
)

func testClassifyClient(body string) *Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, body)
	}))
	client := New(nil)
	client.Endpoint = srv.URL
	client.Session = func(context.Context) (account.Session, error) {
		return account.Session{Pairs: map[string]string{"guid": "guid"}}, nil
	}
	return client
}

func TestClassifyAppUnlisted(t *testing.T) {
	client := testClassifyClient(`{"code":0,"data":{"apps":{}},"msg":""}`)
	got, err := client.ClassifyApp(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listed || got.State != "unlisted" {
		t.Fatalf("listing=%+v", got)
	}
}

func TestClassifyAppListed(t *testing.T) {
	body := `{"code":0,"data":{"apps":{"` + testID + `":{"pkg_name":"` + testID + `","app_name":"茶颜悦色","status":1,"game_type":3}}},"msg":""}`
	client := testClassifyClient(body)
	got, err := client.ClassifyApp(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Listed || got.State != "listed" || got.AppName != "茶颜悦色" || got.GameType != 3 {
		t.Fatalf("listing=%+v", got)
	}
}
