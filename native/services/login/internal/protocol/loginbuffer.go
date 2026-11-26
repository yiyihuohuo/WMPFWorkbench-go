package protocol

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	yybHost            = "https://yybadaccess.3g.qq.com"
	loginBufferURL     = yybHost + "/pc_yyb_auth/pcyyb_get_wx_login_buffer_auth"
	refreshTokenURL    = yybHost + "/pc_yyb/pcyyb_refresh_token"
	sdkRefreshTokenURL = yybHost + "/pc_yyb_auth/pcyyb_refresh_token_auth"
	checkLoginURL      = yybHost + "/pc_yyb/pcyyb_check_login"
	userInfoURL        = yybHost + "/pc_yyb/pcyyb_get_user_info"
	loginBusinessID    = "pc_yyb_auth"
	loginAccessKey     = "wgrdg373hy26ww2"
	sdkScope           = "snsapi_runtime_pcsdk"
)

// Ordinary YYB login and wxapp PCSDK authorization are separate cookie namespaces.
type WXAppCredentials struct {
	OpenID       string `json:"openid"`
	AccessToken  string `json:"accesstoken"`
	RefreshToken string `json:"refreshtoken"`
	LoginType    string `json:"logintype"`
	Scope        string `json:"scope"`
	ExpiresAt    int64  `json:"expires_at"`
	ExpiresIn    int64  `json:"expires_in"`
}
type LoginBufferCredentials struct {
	OpenID       string           `json:"openid"`
	AccessToken  string           `json:"accesstoken"`
	RefreshToken string           `json:"refreshtoken"`
	LoginType    string           `json:"logintype"`
	Nickname     string           `json:"nickname"`
	Scope        string           `json:"scope"`
	AppID        string           `json:"appid"`
	Guid         string           `json:"guid"`
	Cookie       string           `json:"cookie,omitempty"`
	ExpiresAt    int64            `json:"expires_at"`
	ExpiresIn    int64            `json:"expires_in"`
	RefreshedAt  int64            `json:"refresh_refreshed_at"`
	WXApp        WXAppCredentials `json:"wxapp"`
}

func cookieValues(raw string) (map[string]string, []string) {
	values := map[string]string{}
	order := []string{}
	for _, part := range strings.Split(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || name == "" {
			continue
		}
		if _, exists := values[name]; exists {
			continue
		}
		value = strings.Trim(value, "\"")
		if decoded, err := url.PathUnescape(value); err == nil {
			value = decoded
		}
		values[name] = value
		order = append(order, name)
	}
	return values, order
}
func CredentialsFromMap(m map[string]any) LoginBufferCredentials {
	raw := stringFromMap(m, "cookie")
	cookies, _ := cookieValues(raw)
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return cookies[k]
	}
	num := func(k string) int64 {
		if v := int64FromMap(m, k); v != 0 {
			return v
		}
		v, _ := strconv.ParseInt(cookies[k], 10, 64)
		return v
	}
	expiry := num("expires_at")
	if expiry == 0 {
		expiry = num("access_token_expire_time")
	}
	return LoginBufferCredentials{OpenID: str("openid"), AccessToken: str("accesstoken"), RefreshToken: str("refreshtoken"),
		LoginType: defaultString(str("logintype"), "WX"), Nickname: str("nickname"), Scope: str("scope"), AppID: str("appid"),
		Guid: str("guid"), Cookie: raw, ExpiresAt: expiry, ExpiresIn: num("expires_in"), RefreshedAt: num("refresh_refreshed_at"),
		WXApp: WXAppCredentials{OpenID: str("wxapp_openid"), AccessToken: str("wxapp_accesstoken"), RefreshToken: str("wxapp_refreshtoken"),
			LoginType: str("wxapp_logintype"), Scope: str("wxapp_scope"), ExpiresAt: num("wxapp_access_token_expire_time"), ExpiresIn: num("wxapp_expires_in")}}
}
func (c LoginBufferCredentials) ToMap() map[string]any {
	return map[string]any{"openid": c.OpenID, "accesstoken": c.AccessToken, "refreshtoken": c.RefreshToken, "logintype": c.LoginType,
		"nickname": c.Nickname, "scope": c.Scope, "appid": c.AppID, "guid": c.Guid, "cookie": c.CookieHeader(),
		"expires_at": c.ExpiresAt, "expires_in": c.ExpiresIn, "refresh_refreshed_at": c.RefreshedAt,
		"wxapp_openid": c.WXApp.OpenID, "wxapp_accesstoken": c.WXApp.AccessToken, "wxapp_refreshtoken": c.WXApp.RefreshToken,
		"wxapp_logintype": c.WXApp.LoginType, "wxapp_scope": c.WXApp.Scope, "wxapp_access_token_expire_time": c.WXApp.ExpiresAt,
		"wxapp_expires_in": c.WXApp.ExpiresIn}
}
func (c LoginBufferCredentials) CookieHeader() string {
	values, order := cookieValues(c.Cookie)
	set := func(k, v string) {
		if _, ok := values[k]; !ok {
			order = append(order, k)
		}
		values[k] = v
	}
	set("openid", c.OpenID)
	set("accesstoken", c.AccessToken)
	set("refreshtoken", c.RefreshToken)
	set("logintype", c.LoginType)
	set("scope", c.Scope)
	set("guid", c.Guid)
	if c.AppID != "" {
		set("appid", c.AppID)
	}
	if c.Nickname != "" {
		set("nickname", c.Nickname)
	}
	if c.ExpiresAt > 0 {
		set("access_token_expire_time", strconv.FormatInt(c.ExpiresAt, 10))
	}
	if c.ExpiresIn > 0 {
		set("expires_in", strconv.FormatInt(c.ExpiresIn, 10))
	}
	if c.WXApp.OpenID != "" || values["wxapp_openid"] != "" {
		set("wxapp_openid", c.WXApp.OpenID)
		set("wxapp_accesstoken", c.WXApp.AccessToken)
		set("wxapp_refreshtoken", c.WXApp.RefreshToken)
		set("wxapp_logintype", c.WXApp.LoginType)
		set("wxapp_scope", c.WXApp.Scope)
		if c.WXApp.ExpiresAt > 0 {
			set("wxapp_access_token_expire_time", strconv.FormatInt(c.WXApp.ExpiresAt, 10))
		}
	}
	parts := []string{}
	for _, k := range order {
		if values[k] != "" {
			parts = append(parts, k+"="+url.PathEscape(values[k]))
		}
	}
	return strings.Join(parts, "; ")
}
func (c LoginBufferCredentials) Expired(skew time.Duration) bool {
	return c.ExpiresAt <= 0 || time.Now().Add(skew).Unix() >= c.ExpiresAt
}
func hasScope(scope, want string) bool {
	for _, s := range strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		if s == want {
			return true
		}
	}
	return false
}

var ErrScopeRequired = errors.New("PCSDK authorization missing; reauthorize with snsapi_runtime_pcsdk")
var ErrGuidRequired = errors.New("client guid missing; reinitialize or import the original client identity")

type LoginProtocolError struct {
	Stage      string
	Code       int
	HTTPStatus int
}

func (e *LoginProtocolError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("%s HTTP status=%d", e.Stage, e.HTTPStatus)
	}
	return fmt.Sprintf("%s rejected; code=%d", e.Stage, e.Code)
}
func RequiresReauthorization(err error) bool {
	if errors.Is(err, ErrScopeRequired) {
		return true
	}
	var e *LoginProtocolError
	return errors.As(err, &e) && (e.HTTPStatus == 401 || e.HTTPStatus == 403 || e.Code == 40188)
}

type LoginBufferResult struct {
	LoginBuffer string
	Credentials LoginBufferCredentials
	Refreshed   bool
}
type LoginBufferClient struct {
	httpClient *http.Client
	timeout    time.Duration
}

func NewLoginBufferClient(timeout time.Duration) *LoginBufferClient {
	return NewLoginBufferClientWithHTTPClient(timeout, &http.Client{Timeout: timeout})
}

// An explicit client allows bounded contract tests and private deployment transports.
func NewLoginBufferClientWithHTTPClient(timeout time.Duration, hc *http.Client) *LoginBufferClient {
	return &LoginBufferClient{httpClient: hc, timeout: timeout}
}

func (c *LoginBufferClient) FetchLoginBuffer(ctx context.Context, creds LoginBufferCredentials) (string, error) {
	token, scope, typ := creds.AccessToken, creds.Scope, creds.LoginType
	if creds.WXApp.LoginType == "WX" && creds.WXApp.OpenID != "" {
		token, scope, typ = creds.WXApp.AccessToken, creds.WXApp.Scope, creds.WXApp.LoginType
	}
	if typ != "WX" || !hasScope(scope, sdkScope) {
		return "", ErrScopeRequired
	}
	if creds.Guid == "" {
		return "", ErrGuidRequired
	}
	if token == "" {
		return "", errors.New("PCSDK access token missing")
	}
	// Official caller supplies guid/type 1 and omits unionid; [undefined] serializes to [null].
	obj := map[string]any{"extInfo": map[string]any{"listS": map[string]any{
		"unionid": map[string]any{"value": []any{nil}}, "user_id": map[string]any{"value": []string{creds.Guid}}, "access_token": map[string]any{"value": []string{token}}},
		"listI": map[string]any{"user_type": map[string]any{"value": []int{1}}}}}
	data, err := c.post(ctx, "login_buffer", loginBufferURL, loginBusinessID, loginAccessKey, obj, creds)
	if err != nil {
		return "", err
	}
	values := stringSlicePath(data, "ext_info", "list_s", "login_buffer", "value")
	if len(values) == 0 || values[0] == "" {
		return "", errors.New("login_buffer response is empty")
	}
	if decoded, err := base64.StdEncoding.DecodeString(values[0]); err != nil || len(decoded) == 0 {
		return "", errors.New("login_buffer response has invalid encoding")
	}
	return values[0], nil
}

func (c *LoginBufferClient) updateCredentials(ctx context.Context, creds LoginBufferCredentials, check, sdk bool) (LoginBufferCredentials, error) {
	oid, access, refresh, typ := creds.OpenID, creds.AccessToken, creds.RefreshToken, creds.LoginType
	endpoint, business, key, stage := refreshTokenURL, "pc_yyb", "", "refresh_main"
	if check {
		endpoint, stage = checkLoginURL, "check_main"
	}
	if sdk {
		oid, access, refresh, typ = creds.WXApp.OpenID, creds.WXApp.AccessToken, creds.WXApp.RefreshToken, creds.WXApp.LoginType
		endpoint, business, key, stage = sdkRefreshTokenURL, loginBusinessID, loginAccessKey, "refresh_wxapp"
	}
	if oid == "" || typ == "" {
		return creds, errors.New("login identity missing")
	}
	if !check && refresh == "" {
		return creds, errors.New("refresh token missing")
	}
	obj := map[string]any{"userInfo": map[string]any{"openId": oid, "refreshToken": refresh, "accessToken": access, "loginType": typ}}
	data, err := c.post(ctx, stage, endpoint, business, key, obj, creds)
	if err != nil {
		return creds, err
	}
	info, ok := data["user_info"].(map[string]any)
	if !ok {
		return creds, errors.New("credential response missing user_info")
	}
	token := stringFromMap(info, "access_token")
	expires := int64FromMap(info, "expires_in")
	// check_login legitimately returns "0": the official client retains the
	// prior absolute expiry. A check must never restart the token's lifetime.
	preserveExpiry := check && expires == 0 && creds.ExpiresAt > time.Now().Unix()
	if token == "" || (!preserveExpiry && expires <= 0) || expires > 365*86400 {
		return creds, errors.New("credential response missing valid token lifetime")
	}
	if returned := stringFromMap(info, "open_id"); returned != "" && returned != oid {
		return creds, errors.New("credential response identity mismatch")
	}
	if returned := stringFromMap(info, "login_type"); returned != "" && returned != typ {
		return creds, errors.New("credential response login type mismatch")
	}
	rt := stringFromMap(info, "refresh_token")
	now := time.Now().Unix()
	if sdk {
		creds.WXApp.AccessToken = token
		creds.WXApp.ExpiresIn = expires
		creds.WXApp.ExpiresAt = now + expires
		if rt != "" {
			creds.WXApp.RefreshToken = rt
		}
	} else {
		creds.AccessToken = token
		if !preserveExpiry {
			creds.ExpiresIn = expires
			creds.ExpiresAt = now + expires
		}
		if rt != "" {
			creds.RefreshToken = rt
		}
	}
	creds.RefreshedAt = now
	creds.Cookie = creds.CookieHeader()
	return creds, nil
}
func (c *LoginBufferClient) RefreshCredentials(ctx context.Context, creds LoginBufferCredentials) (LoginBufferCredentials, error) {
	return c.updateCredentials(ctx, creds, false, false)
}
func (c *LoginBufferClient) RefreshLoginBuffer(ctx context.Context, creds LoginBufferCredentials) (LoginBufferResult, error) {
	return c.RefreshLoginBufferWithCheckpoint(ctx, creds, nil)
}

// Save each successful credential update before another request can fail or rotate tokens.
func (c *LoginBufferClient) RefreshLoginBufferWithCheckpoint(ctx context.Context, creds LoginBufferCredentials, save func(LoginBufferCredentials) error) (LoginBufferResult, error) {
	result := LoginBufferResult{Credentials: creds}
	checkpoint := func(updated LoginBufferCredentials) error {
		result.Credentials = updated
		result.Refreshed = true
		if save != nil {
			return save(updated)
		}
		return nil
	}
	if creds.OpenID != "" {
		// Two-hour WX tokens need an actual refresh around the one-hour mark;
		// repeated check_login calls do not extend their absolute expiry.
		check := creds.AccessToken != "" && !creds.Expired(time.Hour)
		updated, err := c.updateCredentials(ctx, creds, check, false)
		if err != nil {
			return result, err
		}
		if err = checkpoint(updated); err != nil {
			return result, fmt.Errorf("persist credentials: %w", err)
		}
		creds = updated
	}
	if creds.WXApp.LoginType == "WX" && creds.WXApp.OpenID != "" &&
		(creds.WXApp.AccessToken == "" || creds.WXApp.ExpiresAt <= time.Now().Add(time.Hour).Unix()) {
		updated, err := c.updateCredentials(ctx, creds, false, true)
		if err != nil {
			return result, err
		}
		if err = checkpoint(updated); err != nil {
			return result, fmt.Errorf("persist credentials: %w", err)
		}
		creds = updated
	}
	lb, err := c.FetchLoginBuffer(ctx, creds)
	result.LoginBuffer = lb
	return result, err
}
func (c *LoginBufferClient) FetchUserInfo(ctx context.Context, creds LoginBufferCredentials) (map[string]any, error) {
	typ := "2"
	if creds.LoginType == "QC" {
		typ = "1"
	}
	var data map[string]any
	err := c.requestJSON(ctx, http.MethodGet, userInfoURL, nil, map[string]string{
		"Ual-Access-Access-Token": creds.AccessToken, "Ual-Access-Login-Type": typ, "Ual-Access-Openid": creds.OpenID,
		"Cookie": creds.CookieHeader(), "Origin": "https://pc.yyb.qq.com", "Referer": "https://pc.yyb.qq.com/"}, &data)
	if err != nil {
		return nil, err
	}
	if err = responseCode(data, "ret", "user_info"); err != nil {
		return nil, err
	}
	return data, nil
}
func (c *LoginBufferClient) post(ctx context.Context, stage, endpoint, business, key string, obj any, creds LoginBufferCredentials) (map[string]any, error) {
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, errors.New("encode login request failed")
	}
	ts, nonce := timestampMS(), nonce4()
	var data map[string]any
	err = c.requestJSON(ctx, http.MethodPost, endpoint, body, map[string]string{"Content-Type": "application/json",
		"Ual-Access-Businessid": business, "Ual-Access-Timestamp": ts, "Ual-Access-Nonce": nonce, "Ual-Access-Signature": sign(string(body), nonce, ts, key),
		"Cookie": creds.CookieHeader(), "Origin": "https://pc.yyb.qq.com", "Referer": "https://pc.yyb.qq.com/"}, &data)
	if err != nil {
		return nil, err
	}
	if err = responseCode(data, "code", stage); err != nil {
		return nil, err
	}
	return data, nil
}
func responseCode(data map[string]any, field, stage string) error {
	v, ok := data[field].(float64)
	if !ok || v != float64(int(v)) {
		return errors.New(stage + " response missing numeric status")
	}
	if v != 0 {
		return &LoginProtocolError{Stage: stage, Code: int(v)}
	}
	return nil
}
func (c *LoginBufferClient) requestJSON(ctx context.Context, method, url string, body []byte, headers map[string]string, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return errors.New("invalid login request")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("login transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &LoginProtocolError{Stage: "login", HTTPStatus: resp.StatusCode}
	}
	const maxResponse = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return errors.New("read login response failed")
	}
	if len(data) > maxResponse {
		return errors.New("login response too large")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return errors.New("decode login JSON failed")
	}
	return nil
}

func timestampMS() string {
	return fmt.Sprintf("%d", time.Now().UnixMilli())
}

func nonce4() string {
	return fmt.Sprintf("%d", rand.Intn(10000))
}

func sign(body, nonce, ts, key string) string {
	sum := md5.Sum([]byte(body + ts + key + nonce))
	return hex.EncodeToString(sum[:])
}

func stringSlicePath(m map[string]any, path ...string) []string {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[p]
	}
	arr, ok := cur.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func int64FromMap(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
