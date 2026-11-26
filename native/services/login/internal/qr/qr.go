package qr

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"yyb_go/internal/protocol"
)

const (
	oauthURL     = "https://open.weixin.qq.com/connect/qrconnect"
	callbackURL  = "https://yybadaccess.3g.qq.com/pc_yyb/pcyyb_oauth"
	qrBase       = "https://open.weixin.qq.com/connect/qrcode/"
	longPollBase = "https://long.open.weixin.qq.com/connect/l/qrconnect"
)

var (
	uuidRE = regexp.MustCompile(`/connect/qrcode/([^"'>\s]+)`)
	errRE  = regexp.MustCompile(`wx_errcode\s*=\s*(\d+)`)
	codeRE = regexp.MustCompile(`wx_code\s*=\s*'([^']*)'`)
)

type Session struct {
	ID            string
	WXUUID        string
	QRCodeURL     string
	CreatedAt     time.Time
	HTTPClient    *http.Client
	Jar           http.CookieJar
	AuthorizeCode string
	ClientGuid    string
	callbackDone  bool
	Credentials   *protocol.LoginBufferCredentials
	LoginBuffer   string
	Status        string
	Error         string
	mu            sync.Mutex
}

func (s *Session) Age() time.Duration { return time.Since(s.CreatedAt) }

type ImageResult struct {
	Session    *Session
	ImageBytes []byte
}

type PollResult struct {
	Status  string `json:"status"`
	ErrCode *int   `json:"errcode,omitempty"`
	Code    string `json:"-"`
	Message string `json:"-"`
}

type Client struct {
	timeout      time.Duration
	loginBuffers *protocol.LoginBufferClient
	guidOnce     sync.Once
	guid         string
	guidErr      error
}

func NewClient(timeout time.Duration) *Client {
	return NewClientWithLoginBufferClient(timeout, protocol.NewLoginBufferClient(timeout))
}

func NewClientWithLoginBufferClient(timeout time.Duration, loginBuffers *protocol.LoginBufferClient) *Client {
	return &Client{
		timeout:      timeout,
		loginBuffers: loginBuffers,
	}
}

func (c *Client) LoginBuffers() *protocol.LoginBufferClient { return c.loginBuffers }

func (c *Client) clientGuid() (string, error) {
	c.guidOnce.Do(func() { c.guid, c.guidErr = randomHex(16) })
	if c.guidErr != nil {
		return "", fmt.Errorf("initialize client identity failed")
	}
	return c.guid, nil
}

func (c *Client) GetQRCodeImage(ctx context.Context) (ImageResult, error) {
	sess, err := c.CreateSession(ctx)
	if err != nil {
		return ImageResult{}, err
	}
	data, err := c.FetchQRCodeImage(ctx, sess)
	if err != nil {
		return ImageResult{}, err
	}
	return ImageResult{Session: sess, ImageBytes: data}, nil
}

func (c *Client) CreateSession(ctx context.Context) (*Session, error) {
	guid, err := c.clientGuid()
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	callback, _ := url.Parse(callbackURL)
	jar.SetCookies(callback, []*http.Cookie{{Name: "guid", Value: guid, Path: "/", Secure: true}})
	hc := &http.Client{Timeout: c.timeout, Jar: jar}
	u := oauthURL + "?" + url.Values{
		"appid": {"wxd44977328b36e647"}, "redirect_uri": {callbackURL + "?login_type=WX"},
		"response_type": {"code"}, "scope": {"snsapi_login,snsapi_runtime_pcsdk"},
		"state": {guid}, "fast_login": {"1"}, "self_redirect": {"true"},
	}.Encode() + "#wechat_redirect"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth QR transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OAuth QR HTTP status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read OAuth QR response failed")
	}
	m := uuidRE.FindStringSubmatch(string(body))
	if len(m) < 2 {
		return nil, fmt.Errorf("failed to parse WeChat QR uuid")
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	wxuuid := m[1]
	return &Session{
		ID:         id,
		ClientGuid: guid,
		WXUUID:     wxuuid,
		QRCodeURL:  qrBase + wxuuid,
		CreatedAt:  time.Now(),
		HTTPClient: hc,
		Jar:        jar,
		Status:     "pending",
	}, nil
}

func (c *Client) FetchQRCodeImage(ctx context.Context, sess *Session) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sess.QRCodeURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := sess.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("QR image HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) PollQRCode(ctx context.Context, sess *Session) (PollResult, error) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.LoginBuffer != "" {
		return PollResult{Status: "confirmed"}, nil
	}
	if sess.AuthorizeCode != "" {
		code := 405
		return PollResult{Status: "authorized", ErrCode: &code, Code: sess.AuthorizeCode}, nil
	}
	u := longPollBase + "?" + url.Values{
		"uuid": {sess.WXUUID},
		"_":    {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}.Encode()
	reqCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return PollResult{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := sess.HTTPClient.Do(req)
	if err != nil {
		return PollResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	errcode, code := parsePoll(text)
	switch {
	case errcode != nil && *errcode == 408:
		sess.Status = "pending"
		return PollResult{Status: "pending", ErrCode: errcode, Code: code}, nil
	case errcode != nil && *errcode == 404:
		sess.Status = "scanned"
		return PollResult{Status: "scanned", ErrCode: errcode, Code: code}, nil
	case errcode != nil && *errcode == 403:
		sess.Status = "cancelled"
		return PollResult{Status: "cancelled", ErrCode: errcode, Code: code}, nil
	case errcode != nil && *errcode == 402:
		sess.Status = "expired"
		return PollResult{Status: "expired", ErrCode: errcode, Code: code}, nil
	case errcode != nil && *errcode == 405 && code != "":
		sess.AuthorizeCode = code
		sess.Status = "authorized"
		return PollResult{Status: "authorized", ErrCode: errcode, Code: code}, nil
	default:
		sess.Status = "unknown"
		if len(text) > 200 {
			text = text[:200]
		}
		sess.Error = text
		return PollResult{Status: "unknown", ErrCode: errcode, Code: code, Message: text}, nil
	}
}

func (c *Client) GetLoginBuffer(ctx context.Context, sess *Session) (protocol.LoginBufferResult, error) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.LoginBuffer != "" && sess.Credentials != nil {
		return protocol.LoginBufferResult{LoginBuffer: sess.LoginBuffer, Credentials: *sess.Credentials}, nil
	}
	if sess.AuthorizeCode == "" {
		return protocol.LoginBufferResult{}, fmt.Errorf("QR session is not authorized yet")
	}
	var creds protocol.LoginBufferCredentials
	var err error
	if sess.Credentials != nil {
		creds = *sess.Credentials
	} else {
		if sess.callbackDone {
			return protocol.LoginBufferResult{}, fmt.Errorf("OAuth callback consumed without usable credentials; start a new QR session")
		}
		if sess.ClientGuid == "" {
			sess.ClientGuid, err = c.clientGuid()
			if err != nil {
				return protocol.LoginBufferResult{}, err
			}
		}
		cb := callbackURL + "?" + url.Values{
			"login_type": {"WX"},
			"code":       {sess.AuthorizeCode},
			"state":      {sess.ClientGuid},
		}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cb, nil)
		if err != nil {
			return protocol.LoginBufferResult{}, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Referer", "https://pc.yyb.qq.com/")
		resp, err := sess.HTTPClient.Do(req)
		if err != nil {
			return protocol.LoginBufferResult{}, fmt.Errorf("OAuth callback transport failed")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return protocol.LoginBufferResult{}, fmt.Errorf("OAuth callback HTTP status=%d", resp.StatusCode)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		sess.callbackDone = true
		creds, err = credentialsFromJar(sess.Jar, cb)
		if err != nil {
			return protocol.LoginBufferResult{}, err
		}

		sess.Credentials = &creds
	}
	lb, err := c.loginBuffers.FetchLoginBuffer(ctx, creds)
	if err != nil {
		return protocol.LoginBufferResult{}, err
	}
	sess.Credentials = &creds
	sess.LoginBuffer = lb
	sess.Status = "confirmed"
	return protocol.LoginBufferResult{LoginBuffer: lb, Credentials: creds}, nil
}

func (c *Client) RefreshLoginBufferWithCheckpoint(ctx context.Context, creds protocol.LoginBufferCredentials, save func(protocol.LoginBufferCredentials) error) (protocol.LoginBufferResult, error) {
	return c.loginBuffers.RefreshLoginBufferWithCheckpoint(ctx, creds, save)
}

func (c *Client) RefreshLoginBuffer(ctx context.Context, creds protocol.LoginBufferCredentials) (protocol.LoginBufferResult, error) {
	return c.loginBuffers.RefreshLoginBuffer(ctx, creds)
}

func DataURIJPEG(data []byte) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
}

func parsePoll(text string) (*int, string) {
	var out *int
	if m := errRE.FindStringSubmatch(text); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		out = &n
	}
	code := ""
	if m := codeRE.FindStringSubmatch(text); len(m) == 2 {
		code = m[1]
	}
	return out, code
}

// Use the cookies a browser would send to the ticket endpoint, rather than
// merging callback/root paths with potentially different identities.
func credentialsFromJar(jar http.CookieJar, _ string) (protocol.LoginBufferCredentials, error) {
	target, _ := url.Parse("https://yybadaccess.3g.qq.com/pc_yyb_auth/pcyyb_get_wx_login_buffer_auth")
	parts := []string{}
	seen := map[string]bool{}
	for _, ck := range jar.Cookies(target) {
		key := strings.ToLower(ck.Name)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, key+"="+ck.Value)
	}
	creds := protocol.CredentialsFromMap(map[string]any{"cookie": strings.Join(parts, "; ")})
	if creds.OpenID == "" || creds.AccessToken == "" {
		if creds.WXApp.OpenID == "" || creds.WXApp.AccessToken == "" {
			return creds, fmt.Errorf("OAuth callback did not set required cookies")
		}
	}
	scope := creds.Scope
	if creds.WXApp.LoginType == "WX" && creds.WXApp.OpenID != "" {
		scope = creds.WXApp.Scope
	}
	granted := false
	for _, s := range strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		granted = granted || s == "snsapi_runtime_pcsdk"
	}
	if !granted {
		return creds, protocol.ErrScopeRequired
	}
	if creds.Guid == "" {
		guid, err := randomHex(16)
		if err != nil {
			return creds, fmt.Errorf("initialize client identity failed")
		}
		creds.Guid = guid
		jar.SetCookies(target, []*http.Cookie{{Name: "guid", Value: guid, Path: "/", Secure: true, MaxAge: 90 * 86400}})
	}
	now := time.Now().Unix()
	if creds.ExpiresAt == 0 && creds.AccessToken != "" {
		if creds.ExpiresIn <= 0 {
			creds.ExpiresIn = 7200
		}
		creds.ExpiresAt = now + creds.ExpiresIn
	}
	if creds.WXApp.ExpiresAt == 0 && creds.WXApp.AccessToken != "" {
		if creds.WXApp.ExpiresIn <= 0 {
			creds.WXApp.ExpiresIn = 7200
		}
		creds.WXApp.ExpiresAt = now + creds.WXApp.ExpiresIn
	}
	creds.Cookie = creds.CookieHeader()
	return creds, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
