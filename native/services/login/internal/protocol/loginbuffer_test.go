package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type loginRoundTrip func(*http.Request) (*http.Response, error)

func (f loginRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func sampleCredentials() LoginBufferCredentials {
	return CredentialsFromMap(map[string]any{"openid": "main-open", "accesstoken": "main-new", "refreshtoken": "main-refresh", "logintype": "WX", "scope": "snsapi_login,snsapi_runtime_pcsdk", "guid": "client-guid", "expires_at": time.Now().Add(2 * time.Hour).Unix(), "cookie": "guid=client-guid; scope=snsapi_login,snsapi_runtime_pcsdk; openid=main-open; accesstoken=main-old; refreshtoken=old; custom=keep"})
}
func cookieMap(r *http.Request) map[string]string {
	v := map[string]string{}
	for _, c := range r.Cookies() {
		v[c.Name] = c.Value
	}
	return v
}

func TestCookieRebuildUsesRefreshedTokenAndActualScope(t *testing.T) {
	c := sampleCredentials()
	r, _ := http.NewRequest("GET", "https://example.test", nil)
	r.Header.Set("Cookie", c.CookieHeader())
	p := cookieMap(r)
	if p["accesstoken"] != "main-new" || p["refreshtoken"] != "main-refresh" || p["custom"] != "keep" {
		t.Fatal("Cookie must use current structured credentials and retain unrelated fields")
	}
	c.Scope = "snsapi_login"
	m := c.ToMap()
	if strings.Contains(m["scope"].(string), "runtime_pcsdk") {
		t.Fatal("must not invent authorization")
	}
}

func TestFetchTicketMatchesOfficialGuidContract(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		x := b["extInfo"].(map[string]any)
		ss := x["listS"].(map[string]any)
		ii := x["listI"].(map[string]any)
		get := func(k string) any { return ss[k].(map[string]any)["value"].([]any)[0] }
		if get("user_id") != "client-guid" || get("unionid") != nil || ii["user_type"].(map[string]any)["value"].([]any)[0] != float64(1) {
			t.Error("ticket identity must be guid/type 1 with null omitted unionid")
		}
		return reply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
	})
	if _, err := c.FetchLoginBuffer(context.Background(), sampleCredentials()); err != nil {
		t.Fatal(err)
	}
}

func TestMissingScopeRejectedBeforeNetwork(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	called := false
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) { called = true; return reply(`{}`), nil })
	creds := sampleCredentials()
	creds.Scope = "snsapi_login"
	_, err := c.FetchLoginBuffer(context.Background(), creds)
	if err == nil || called {
		t.Fatal("missing authorization must not be fixed by editing cookie text")
	}
}

func TestRefreshUsesMainEndpointCookieAndUpdatedCookie(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/pc_yyb/pcyyb_refresh_token" {
			t.Error("main session must use the ordinary refresh endpoint")
		}
		if r.Header.Get("Cookie") == "" {
			t.Error("official helper includes browser cookies")
		}
		return reply(`{"code":0,"user_info":{"access_token":"rotated","refresh_token":"rotated-refresh","expires_in":7200}}`), nil
	})
	got, err := c.RefreshCredentials(context.Background(), sampleCredentials())
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", "https://example.test", nil)
	r.Header.Set("Cookie", got.CookieHeader())
	if cookieMap(r)["accesstoken"] != "rotated" {
		t.Fatal("rotated token was overridden by stale raw Cookie")
	}
}

func TestPartialRefreshSurvivesTicketFailure(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "check_login") || strings.Contains(r.URL.Path, "refresh_token") {
			return reply(`{"code":0,"user_info":{"access_token":"rotated","refresh_token":"rotated-refresh","expires_in":7200}}`), nil
		}
		return nil, errors.New("upstream temporarily unavailable SECRET")
	})
	result, err := c.RefreshLoginBuffer(context.Background(), sampleCredentials())
	if err == nil {
		t.Fatal("expected ticket failure")
	}
	if result.Credentials.AccessToken != "rotated" {
		t.Fatal("successful credential update was lost after ticket failure")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("transport error leaked upstream content")
	}
}

func TestInvalidResponsesFailWithoutLeakingBody(t *testing.T) {
	for _, body := range []string{`{"user_info":{"access_token":"SECRET"}}`, `{"code":0}`, `{"code":99,"msg":"SECRET"}`, `SECRET`} {
		c := NewLoginBufferClient(time.Second)
		c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) { return reply(body), nil })
		_, err := c.RefreshCredentials(context.Background(), sampleCredentials())
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("invalid response accepted or sensitive response leaked")
		}
	}
}

func TestSdkAuthorizationUsesSeparateWxappCredentials(t *testing.T) {
	m := sampleCredentials().ToMap()
	m["scope"] = "snsapi_login"
	m["wxapp_logintype"] = "WX"
	m["wxapp_openid"] = "sdk-open"
	m["wxapp_accesstoken"] = "sdk-token"
	m["wxapp_refreshtoken"] = "sdk-refresh"
	m["wxapp_scope"] = "snsapi_runtime_pcsdk"
	m["wxapp_access_token_expire_time"] = time.Now().Add(2 * time.Hour).Unix()
	creds := CredentialsFromMap(m)
	client := NewLoginBufferClient(time.Second)
	client.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		s := b["extInfo"].(map[string]any)["listS"].(map[string]any)
		if s["access_token"].(map[string]any)["value"].([]any)[0] != "sdk-token" {
			t.Error("PCSDK ticket must select separate wxapp credentials")
		}
		if cookieMap(r)["accesstoken"] != "main-new" || cookieMap(r)["wxapp_accesstoken"] != "sdk-token" {
			t.Error("main and wxapp cookie namespaces must stay separate")
		}
		return reply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
	})
	if _, err := client.FetchLoginBuffer(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	round := CredentialsFromMap(creds.ToMap())
	if _, err := client.FetchLoginBuffer(context.Background(), round); err != nil {
		t.Fatal(err)
	}
}

func TestImportDecodesBrowserCookieEscapingWithoutInventingScope(t *testing.T) {
	c := CredentialsFromMap(map[string]any{"cookie": "logintype=WX; guid=g; openid=o; accesstoken=1_a%2Bb; refreshtoken=r; scope=snsapi_login%2Csnsapi_runtime_pcsdk; nickname=%E6%B5%8B%E8%AF%95"})
	if c.Scope != "snsapi_login,snsapi_runtime_pcsdk" || c.AccessToken != "1_a+b" || c.Nickname != "测试" {
		t.Fatal("browser Cookie values must be percent-decoded on import")
	}
	round := CredentialsFromMap(map[string]any{"cookie": c.CookieHeader()})
	if round.Scope != c.Scope || round.AccessToken != c.AccessToken || round.Nickname != c.Nickname {
		t.Fatal("browser cookie round trip changed credential values")
	}
}

func TestRefreshAcceptsOfficialStringExpiryAndRetainsEmptyIdentity(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		return reply(`{"code":0,"user_info":{"access_token":"new","refresh_token":"new-refresh","expires_in":"7200","open_id":"","login_type":"WX","guid":""}}`), nil
	})
	got, err := c.RefreshCredentials(context.Background(), sampleCredentials())
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresIn != 7200 || got.OpenID != "main-open" || got.Guid != "client-guid" {
		t.Fatal("official sparse refresh response must retain existing identity and parse string expiry")
	}
}

func TestCheckPreservesAbsoluteExpiryWhenServerReturnsZero(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "check_login") {
			return reply(`{"code":0,"user_info":{"access_token":"checked","refresh_token":"checked-refresh","expires_in":"0","open_id":"main-open","login_type":"WX"}}`), nil
		}
		return reply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
	})
	creds := sampleCredentials()
	got, err := c.RefreshLoginBuffer(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	if got.Credentials.ExpiresAt != creds.ExpiresAt || got.Credentials.AccessToken != "checked" {
		t.Fatal("a successful check must retain the original absolute expiry without extending it")
	}
}

func TestRenewalRefreshesBeforeMainTokenExpires(t *testing.T) {
	creds := sampleCredentials()
	creds.ExpiresAt = time.Now().Add(30 * time.Minute).Unix()
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "login_buffer") {
			return reply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
		}
		if r.URL.Path != "/pc_yyb/pcyyb_refresh_token" {
			t.Error("main token nearing expiry needs refresh, not repeated checks")
		}
		return reply(`{"code":0,"user_info":{"access_token":"renewed","expires_in":"7200"}}`), nil
	})
	if _, err := c.RefreshLoginBuffer(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
}

func TestWxappRefreshHasSeparateSignatureAndDurableCheckpoint(t *testing.T) {
	creds := sampleCredentials()
	creds.OpenID = ""
	creds.WXApp = WXAppCredentials{OpenID: "sdk-open", LoginType: "WX", AccessToken: "old-sdk", RefreshToken: "sdk-refresh", Scope: sdkScope, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	saved := false
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "login_buffer") {
			if !saved {
				t.Error("wxapp credentials must be durable before ticket request")
			}
			return reply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
		}
		body, _ := io.ReadAll(r.Body)
		expected := sign(string(body), r.Header.Get("Ual-Access-Nonce"), r.Header.Get("Ual-Access-Timestamp"), loginAccessKey)
		if r.URL.Path != "/pc_yyb_auth/pcyyb_refresh_token_auth" || r.Header.Get("Ual-Access-Businessid") != loginBusinessID || r.Header.Get("Ual-Access-Signature") != expected {
			t.Error("wxapp refresh must use its auth endpoint, business and signing key")
		}
		return reply(`{"code":0,"user_info":{"access_token":"new-sdk","expires_in":"7200"}}`), nil
	})
	got, err := c.RefreshLoginBufferWithCheckpoint(context.Background(), creds, func(updated LoginBufferCredentials) error {
		saved = updated.WXApp.AccessToken == "new-sdk" && updated.AccessToken == creds.AccessToken
		return nil
	})
	if err != nil || got.Credentials.WXApp.RefreshToken != "sdk-refresh" {
		t.Fatal("wxapp renewal failed or lost sparse refresh token")
	}
}

func TestCheckpointFailureStopsBeforeTicketAndReturnsRotatedCredentials(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "login_buffer") {
			t.Fatal("must not continue after checkpoint failure")
		}
		return reply(`{"code":0,"user_info":{"access_token":"new","expires_in":"7200"}}`), nil
	})
	got, err := c.RefreshLoginBufferWithCheckpoint(context.Background(), sampleCredentials(), func(LoginBufferCredentials) error { return errors.New("disk unavailable") })
	if err == nil || got.Credentials.AccessToken != "new" {
		t.Fatal("caller must receive rotated credentials even if saving fails")
	}
}

func TestMalformedTicketIsNotAcceptedAsAuthenticatedState(t *testing.T) {
	c := NewLoginBufferClient(time.Second)
	c.httpClient.Transport = loginRoundTrip(func(r *http.Request) (*http.Response, error) {
		return reply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["invalid!ticket"]}}}}`), nil
	})
	if _, err := c.FetchLoginBuffer(context.Background(), sampleCredentials()); err == nil {
		t.Fatal("malformed login buffer must not be persisted as alive")
	}
}
