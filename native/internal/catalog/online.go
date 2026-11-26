package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"wmpfworkbench/internal/account"
)

var AppID = regexp.MustCompile(`^wx[0-9a-f]{16}$`)

type Online struct {
	Account  *account.Client
	HTTP     *http.Client
	Endpoint string
}

func NewOnline(a *account.Client) *Online {
	return &Online{a, account.HTTPClient(25 * time.Second), "https://yybadaccess.3g.qq.com/v2/dynamicard_pcyyb?scene=search_result"}
}
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func number(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, err := strconv.Atoi(n)
		if err == nil {
			return i
		}
	}
	return -1
}
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func truth(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case float64:
		return x != 0
	}
	return true
}
func BuildRequest(keyword string, pairs map[string]string) map[string]any {
	listS := map[string]any{}
	for key, val := range map[string]string{"keyword": keyword, "region": "CN", "supplyId": "2100100018", "wx_sdk_version": "5.10.7300.414", "architecture": "x64"} {
		listS[key] = map[string]any{"repStr": []string{val}}
	}
	listI := map[string]any{}
	for key, val := range map[string]int{"international": 0, "client_type": 0, "oem_type": 0, "multi_oaid_switch": 1, "wechat_authorization_status": 1, "support_wechat_playable_ad": 1} {
		listI[key] = map[string]any{"repInt": []int{val}}
	}
	return map[string]any{"head": map[string]any{"cmd": "dynamicard_pcyyb", "userInfo": map[string]any{"guid": pairs["guid"], "qimei36": ""}, "authInfo": map[string]any{"businessId": "AuthName", "qqOpenId": "", "A2": "", "weixinOpenId": pairs["openid"], "weixinAccessToken": pairs["accesstoken"], "authType": 2}, "hostAppInfo": map[string]any{"versionName": "5.7700.6773", "scene": "search_result"}}, "body": map[string]any{"bid": "yybpcclient", "preview": false, "layout": "", "listS": listS, "listI": listI, "offset": 0, "size": 3, "trace": false}}
}
func Normalize(value map[string]any, keyword string, limit int) (map[string]any, error) {
	fail := func() (map[string]any, error) {
		return nil, errors.New("Search response envelope or card metadata invalid")
	}
	ret, ok := value["ret"].(float64)
	if !ok {
		return fail()
	}
	if ret != 0 {
		return nil, fmt.Errorf("Search rejected; ret=%v", ret)
	}
	data := obj(value["data"])
	components, ok := data["components"].([]any)
	if !ok || truth(data["errors"]) {
		return fail()
	}
	rows := []map[string]any{}
	seen := map[string]bool{}
	sourceCount, cards := 0, 0
	for _, component := range components {
		card := obj(component)
		if card["cardId"] != "SearchResult" {
			continue
		}
		cards++
		if truth(card["errors"]) || truth(card["sourceErrors"]) {
			return fail()
		}
		items, ok := obj(card["data"])["itemData"].([]any)
		if !ok {
			return fail()
		}
		sourceCount += len(items)
		for _, raw := range items {
			item := obj(raw)
			if item == nil {
				return fail()
			}
			appid := str(item["pkg_name"])
			kind := number(item["game_type"])
			if !AppID.MatchString(appid) || (kind != 2 && kind != 3) || seen[appid] {
				continue
			}
			seen[appid] = true
			name := str(item["name"])
			kindName := "mini_program"
			if kind == 2 {
				kindName = "mini_game"
			}
			nameMatch := strings.Contains(strings.ToLower(name), strings.ToLower(keyword))
			match := "recommended"
			if appid == keyword {
				match = "appid"
			} else if nameMatch {
				match = "name"
			}
			matchNote := map[string]string{"name": "名称包含关键词", "appid": "AppID 精确匹配", "recommended": "服务端推荐/其他匹配"}[match]
			rows = append(rows, map[string]any{"appid": appid, "name": name, "game_type": kind, "kind": kindName, "status": str(item["status"]), "online": str(item["status"]) == "1", "name_contains_keyword": nameMatch || keyword == appid, "match": match, "match_note": matchNote})
		}
	}
	offset, size, total := number(data["offset"]), number(data["size"]), number(data["total"])
	if cards == 0 || offset < 0 || total < 0 || size <= 0 {
		return fail()
	}
	available := len(rows)
	rows = rows[:min(len(rows), limit)]
	matches := 0
	for _, row := range rows {
		if row["name_contains_keyword"] == true {
			matches++
		}
	}
	var next any
	if offset+size < total {
		next = offset + size
	}
	return map[string]any{"keyword": keyword, "source": "official_online_search", "grade": "confirmed", "hits": len(rows), "items": rows, "available_mini_programs": available, "server_search_items": sourceCount, "truncated": available > limit, "name_match_count": matches, "pagination": map[string]any{"unit": "cards", "offset": offset, "size": size, "total": total, "next_card_offset": next}, "catalog_exhaustive": false, "note": "服务端搜索卡片可能含相关或推广结果；limit 仅裁剪本次结果，未验证应用级翻页。"}, nil
}
func (o *Online) Search(ctx context.Context, keyword string, limit int) (map[string]any, error) {
	if strings.TrimSpace(keyword) == "" || len([]rune(keyword)) > 200 || strings.IndexFunc(keyword, unicode.IsControl) >= 0 || limit < 1 || limit > 100 {
		return nil, errors.New("keyword requires 1..200 characters; limit 1..100")
	}
	keyword = strings.TrimSpace(keyword)
	session, err := o.Account.Session(ctx)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	enc.Encode(BuildRequest(keyword, session.Pairs))
	req, err := http.NewRequestWithContext(ctx, "POST", o.Endpoint, &body)
	if err != nil {
		return nil, errors.New("Search transport failed")
	}
	for k, v := range map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Cookie": session.Cookie, "User-Agent": "Mozilla/5.0", "Origin": "https://pc.yyb.qq.com", "Referer": "https://pc.yyb.qq.com/"} {
		req.Header.Set(k, v)
	}
	response, err := o.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("Search transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Search HTTP status=%d", response.StatusCode)
	}
	var value map[string]any
	if json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&value) != nil {
		return nil, errors.New("Search returned invalid JSON")
	}
	result, err := Normalize(value, keyword, limit)
	if err == nil {
		result["auth_source"] = map[string]any{"provider": "desktop_login", "account_selected": true}
	}
	return result, err
}
