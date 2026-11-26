package launch

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"wmpfworkbench/internal/account"
)

func TestRefreshAccessTokenFromBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pc_yyb/pcyyb_refresh_token" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Ual-Access-Openid") != "" {
			t.Errorf("main refresh must not send openid header")
		}
		if r.Header.Get("Cookie") != "" {
			t.Errorf("main refresh must not send Cookie")
		}
		if r.Header.Get("Ual-Access-Businessid") != "pc_yyb" {
			t.Errorf("business=%s", r.Header.Get("Ual-Access-Businessid"))
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte("refreshToken")) {
			t.Errorf("body missing refreshToken: %s", body)
		}
		nonce := r.Header.Get("Ual-Access-Nonce")
		ts := r.Header.Get("Ual-Access-Timestamp")
		want := fmt.Sprintf("%x", md5.Sum(append(append(append([]byte{}, body...), []byte(ts)...), []byte(nonce)...)))
		if r.Header.Get("Ual-Access-Signature") != want {
			t.Errorf("signature must use empty key")
		}
		io.WriteString(w, `{"ret":0,"code":0,"msg":"RC_SUCCESS","user_info":{"access_token":"NEW_TOKEN","refresh_token":"NEW_REFRESH","openid":""}}`)
	}))
	defer srv.Close()
	c := New(nil)
	c.Endpoint = srv.URL
	sess := account.Session{Cookie: "accesstoken=OLD; openid=owTEST", SDKToken: "OLD",
		Pairs: map[string]string{"openid": "owTEST", "refreshtoken": "REF", "logintype": "WX"}}
	tok, err := c.refreshAccessToken(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "NEW_TOKEN" {
		t.Fatalf("token=%q", tok)
	}
	if strings.Contains(sess.Cookie, "NEW_TOKEN") {
		t.Fatal("session mutated unexpectedly")
	}
}

func TestReplaceCookieValue(t *testing.T) {
	got := replaceCookieValue("accesstoken=OLD; openid=owX; scope=a,b", "accesstoken", "NEW_1")
	if !strings.Contains(got, "accesstoken=NEW_1") || !strings.Contains(got, "openid=owX") {
		t.Fatalf("cookie=%s", got)
	}
}
