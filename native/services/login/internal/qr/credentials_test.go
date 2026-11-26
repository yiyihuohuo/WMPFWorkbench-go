package qr

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"yyb_go/internal/protocol"
)

type qrTransport func(*http.Request) (*http.Response, error)

func (f qrTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthUsesStableClientGuidAndDoesNotRedeemCodeAfterTicketFailure(t *testing.T) {
	var state string
	callbacks, tickets := 0, 0
	transport := qrTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		header := http.Header{}
		switch r.URL.Path {
		case "/connect/qrconnect":
			got := r.URL.Query().Get("state")
			if got == "" || got == "web" || (state != "" && state != got) {
				t.Error("OAuth state must be a stable client guid initialized before QR creation")
			}
			state = got
			body = `<img src="/connect/qrcode/test-uuid">`
		case "/pc_yyb/pcyyb_oauth":
			callbacks++
			if r.URL.Query().Get("state") != state {
				t.Error("callback must retain original OAuth state")
			}
			for _, ck := range []string{"openid=o; Path=/", "accesstoken=a; Path=/", "refreshtoken=r; Path=/", "logintype=WX; Path=/", "scope=snsapi_login%2Csnsapi_runtime_pcsdk; Path=/"} {
				header.Add("Set-Cookie", ck)
			}
		default:
			tickets++
			if tickets == 1 {
				return nil, errors.New("offline")
			}
			body = `{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	client := NewClientWithLoginBufferClient(time.Second, protocol.NewLoginBufferClientWithHTTPClient(time.Second, &http.Client{Transport: transport}))
	sess, err := client.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	sess.AuthorizeCode = "one-use-code"
	if _, err := client.GetLoginBuffer(context.Background(), sess); err == nil {
		t.Fatal("expected first ticket failure")
	}
	got, err := client.GetLoginBuffer(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 || tickets != 2 || got.Credentials.Guid != state {
		t.Fatal("ticket retry must reuse saved credentials and original client identity")
	}
}

func TestOAuthCallbackErrorDoesNotExposeOneUseCode(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	sess := &Session{AuthorizeCode: "SECRET-CODE", Jar: jar, HTTPClient: &http.Client{Transport: qrTransport(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("transport detail SECRET-CODE")
	})}}
	_, err := NewClient(time.Second).GetLoginBuffer(context.Background(), sess)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("callback transport error must redact OAuth URL and code")
	}
}

func TestJarUsesTicketEndpointScopeAndOneConsistentValue(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	root, _ := url.Parse("https://yybadaccess.3g.qq.com/")
	jar.SetCookies(root, []*http.Cookie{
		{Name: "openid", Value: "root-open", Path: "/"}, {Name: "accesstoken", Value: "root-token", Path: "/"},
		{Name: "scope", Value: "snsapi_login,snsapi_runtime_pcsdk", Path: "/"}, {Name: "guid", Value: "stable-guid", Path: "/"},
		{Name: "openid", Value: "wrong-path-open", Path: "/pc_yyb"}, {Name: "accesstoken", Value: "wrong-path-token", Path: "/pc_yyb"},
	})
	creds, err := credentialsFromJar(jar, "https://yybadaccess.3g.qq.com/pc_yyb/pcyyb_oauth?code=private")
	if err != nil {
		t.Fatal(err)
	}
	if creds.OpenID != "root-open" || creds.AccessToken != "root-token" || strings.Contains(creds.CookieHeader(), "wrong-path") {
		t.Fatal("callback-only cookies must not override actual ticket endpoint cookies")
	}
	if creds.ToMap()["guid"] != "stable-guid" {
		t.Fatal("client identity was lost")
	}
}

func TestJarDoesNotFabricateSdkScope(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	root, _ := url.Parse("https://yybadaccess.3g.qq.com/")
	jar.SetCookies(root, []*http.Cookie{{Name: "openid", Value: "o", Path: "/"}, {Name: "accesstoken", Value: "a", Path: "/"}, {Name: "scope", Value: "snsapi_login", Path: "/"}})
	_, err := credentialsFromJar(jar, root.String())
	if err == nil {
		t.Fatal("insufficient grant must require authorization, not fabricated scope")
	}
}

func TestJarInitializesAndKeepsClientGuid(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	root, _ := url.Parse("https://yybadaccess.3g.qq.com/")
	jar.SetCookies(root, []*http.Cookie{{Name: "openid", Value: "o", Path: "/"}, {Name: "accesstoken", Value: "a", Path: "/"}, {Name: "scope", Value: "snsapi_runtime_pcsdk", Path: "/"}})
	first, err := credentialsFromJar(jar, root.String())
	if err != nil {
		t.Fatal(err)
	}
	second, err := credentialsFromJar(jar, root.String())
	if err != nil {
		t.Fatal(err)
	}
	if first.ToMap()["guid"] == "" || first.ToMap()["guid"] != second.ToMap()["guid"] {
		t.Fatal("a standalone client must persist one generated guid")
	}
}
