// Package launch prepares inputs for the owned C++ host, without logging tickets.
package launch

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"wmpfworkbench/internal/account"
	"wmpfworkbench/internal/app"
)

type Client struct {
	Session  func(context.Context) (account.Session, error)
	HTTP     *http.Client
	Endpoint string
}

func New(a *account.Client) *Client {
	c := &Client{HTTP: account.HTTPClient(25 * time.Second), Endpoint: "https://yybadaccess.3g.qq.com"}
	if a != nil {
		c.Session = a.Session
	}
	return c
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func text(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func integer(v any) int { n, _ := strconv.Atoi(text(v)); return n }
func field(num uint64, value []byte) []byte {
	out := binary.AppendUvarint(nil, num<<3|2)
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}
func BuildConfig(id string, encrypted map[string]any) ([]byte, []byte, error) {
	if !app.AppID.MatchString(id) {
		return nil, nil, errors.New("invalid appid")
	}
	f27 := field(1, []byte("wxd44977328b36e647"))
	for i, key := range []string{"iv", "data", "timestamp", "authtag"} {
		v, ok := encrypted[key].(string)
		if !ok || v == "" || len(v) > 4<<20 {
			return nil, nil, errors.New("启动信封字段无效")
		}
		f27 = append(f27, field(uint64(i+2), []byte(v))...)
	}
	cfg := field(1, []byte(id))
	cfg = append(cfg, field(8, []byte(`{"require_hwnd_notify":true}`))...)
	cfg = append(cfg, field(27, f27)...)
	return cfg, f27, nil
}
func (c *Client) post(ctx context.Context, session account.Session, path string, value any, business, key, salt string) (map[string]any, error) {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, errors.New("启动参数编码失败")
	}
	raw := bytes.TrimSuffix(body.Bytes(), []byte{'\n'})
	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint+path, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("启动请求地址无效")
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return nil, errors.New("请求随机数生成失败")
	}
	var requestID [8]byte
	if _, err = rand.Read(requestID[:]); err != nil {
		return nil, errors.New("请求标识生成失败")
	}
	for key, val := range map[string]string{"Content-Type": "application/json", "Cookie": session.Cookie, "User-Agent": "Mozilla/5.0", "Origin": "https://pc.yyb.qq.com", "Referer": "https://pc.yyb.qq.com/", "Ual-Access-Guid": session.Pairs["guid"], "Ual-Access-Businessid": business, "Ual-Access-Timestamp": ts, "Ual-Access-Nonce": nonce.String(), "Ual-Access-Requestid": hex.EncodeToString(requestID[:])} {
		req.Header.Set(key, val)
	}
	if key != "" {
		hash := md5.Sum([]byte(string(raw) + ts + salt + key + nonce.String()))
		req.Header.Set("Ual-Access-Signature", hex.EncodeToString(hash[:]))
	}
	// The app-store endpoints are slow and flaky (observed 8-17s per call, with
	// occasional timeouts). Retry transport failures and 5xx with a fresh body
	// and nonce — a timeout must not fail the whole launch. Business errors
	// (4xx / non-zero code) are returned immediately: retrying them is useless.
	var res *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			req.Body = io.NopCloser(bytes.NewReader(raw))
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
		}
		res, err = c.HTTP.Do(req)
		if err == nil && res.StatusCode < 500 {
			break
		}
		if res != nil {
			res.Body.Close()
		}
		if attempt == 2 {
			if err != nil {
				return nil, fmt.Errorf("启动服务连接失败或请求超时（重试3次）: %w", err)
			}
			return nil, fmt.Errorf("启动服务 HTTP %d（重试3次）", res.StatusCode)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("启动服务 HTTP %d", res.StatusCode)
	}
	rawResponse, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil || len(rawResponse) > 16<<20 {
		return nil, errors.New("启动服务响应超限或读取失败")
	}
	var response map[string]any
	if json.Unmarshal(rawResponse, &response) != nil || response == nil {
		return nil, errors.New("启动服务返回无效 JSON")
	}
	if code, ok := response["code"]; ok {
		if integer(code) != 0 || text(code) != "0" {
			return nil, errors.New("启动服务拒绝请求")
		}
	}
	return response, nil
}
func (c *Client) Mint(ctx context.Context, id, entry, dest string) (map[string]any, error) {
	return c.mint(ctx, id, entry, dest, false)
}

// MintFresh ignores the login buffer cached in the local session and fetches a
// new one from the authenticated login-buffer endpoint. It is intended for a
// single recovery attempt after Runtime rejects the previous ticket.
func (c *Client) MintFresh(ctx context.Context, id, entry, dest string) (map[string]any, error) {
	return c.mint(ctx, id, entry, dest, true)
}

func (c *Client) mint(ctx context.Context, id, entry, dest string, freshTicket bool) (map[string]any, error) {
	if !app.AppID.MatchString(id) || len(entry) > 4096 {
		return nil, errors.New("appid 或启动路径不合法")
	}
	if c.Session == nil {
		return nil, errors.New("登录服务尚未配置")
	}
	session, err := c.Session(ctx)
	if err != nil {
		return nil, err
	}
	detail, err := c.post(ctx, session, "/pc_yyb_client/pcyyb_get_app_detail", map[string]any{"env_info": map[string]any{"guid": session.Pairs["guid"], "media_channel": "2100100018"}, "pkg_name": id}, "pc_yyb", "", "")
	if err != nil {
		return nil, err
	}
	var info map[string]any
	for _, v := range object(object(detail["data"])["apps"]) {
		if candidate := object(v); candidate["pkg_name"] == id {
			info = candidate
			break
		}
	}
	if info == nil {
		return nil, errors.New("服务端详情中没有请求的小程序")
	}
	if integer(info["status"]) != 1 {
		return nil, errors.New("小程序当前未上架")
	}
	kind := integer(info["game_type"])
	if kind != 2 && kind != 3 {
		return nil, errors.New("服务端详情未提供有效的小程序类型")
	}
	encrypted, err := c.post(ctx, session, "/v3/pcyyb_encrypt_wx_mini_program_launch_param", map[string]any{"app_id": id, "entry_url": entry, "host_scene": "2100100018"}, "pc_yyb", "84e752c6ed95c057", "df683a7f1c814b4f")
	if err != nil {
		return nil, err
	}
	cfg, f27, err := BuildConfig(id, object(encrypted["encrypt_param"]))
	if err != nil {
		return nil, err
	}
	scoped := false
	scope := strings.ReplaceAll(session.SDKScope, "%2C", ",")
	for _, s := range strings.Split(scope, ",") {
		if strings.TrimSpace(s) == "snsapi_runtime_pcsdk" {
			scoped = true
		}
	}
	if !scoped {
		return nil, errors.New("需要实际 PCSDK 授权")
	}
	ticket := session.LoginBuffer
	if freshTicket || ticket == "" {
		if session.SDKToken == "" {
			return nil, errors.New("PCSDK token 缺失")
		}
		res, err := c.post(ctx, session, "/pc_yyb_auth/pcyyb_get_wx_login_buffer_auth", map[string]any{"extInfo": map[string]any{"listS": map[string]any{"unionid": map[string]any{"value": []any{nil}}, "user_id": map[string]any{"value": []string{session.Pairs["guid"]}}, "access_token": map[string]any{"value": []string{session.SDKToken}}}, "listI": map[string]any{"user_type": map[string]any{"value": []int{1}}}}}, "pc_yyb_auth", "wgrdg373hy26ww2", "")
		if err != nil {
			return nil, err
		}
		values, _ := object(object(object(res["ext_info"])["list_s"])["login_buffer"])["value"].([]any)
		if len(values) > 0 {
			ticket, _ = values[0].(string)
		}
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(ticket)
	if err != nil || len(decoded) == 0 {
		return nil, errors.New("登录票据格式无效")
	}
	cfgHash, ticketHash := sha256.Sum256(cfg), sha256.Sum256([]byte(ticket))
	meta := map[string]any{"appid": id, "app_name": info["app_name"], "game_type": kind, "entry_empty": entry == "", "minted_at": time.Now().Format("2006-01-02 15:04:05"), "cfg_bytes": len(cfg), "f27_bytes": len(f27), "login_buffer_chars": len(ticket), "cfg_sha256": hex.EncodeToString(cfgHash[:]), "login_sha256": hex.EncodeToString(ticketHash[:]), "provider": "desktop_login"}
	// Publish a directory only after all responses and formats pass. An existing
	// session directory is never reused, preventing a mixed old/new ticket pair.
	dest, err = filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("启动输入目录已存在或不可访问")
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".launch-inputs-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	for name, data := range map[string][]byte{"launch.bin": cfg, "login.txt": []byte(ticket), "game_type.txt": []byte(strconv.Itoa(kind))} {
		if err = os.WriteFile(filepath.Join(tmp, name), data, 0600); err != nil {
			return nil, errors.New("无法写入启动输入")
		}
	}
	if err = app.WriteJSON(filepath.Join(tmp, "metadata.json"), meta); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = os.Rename(tmp, dest); err != nil {
		return nil, errors.New("无法发布启动输入")
	}
	return meta, nil
}
