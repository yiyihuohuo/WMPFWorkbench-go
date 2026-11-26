package dynamic

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type ScanRule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Severity string `json:"severity"`
	Enabled  bool   `json:"enabled"`
}

type Finding struct {
	Rule        string `json:"rule"`
	Severity    string `json:"severity"`
	File        string `json:"file"`
	FileDisplay string `json:"file_display"`
	Line        int    `json:"line"`
	Column      int    `json:"column"`
	Snippet     string `json:"snippet"`
	Match       string `json:"match"`
	SourceType  string `json:"source_type"`
}

type ScanStats struct {
	FilesScanned int `json:"files_scanned"`
	Findings     int `json:"findings"`
}

func (m *Manager) rulesPath() string { return m.path("scan-rules.json") }

func defaultScanRules() []ScanRule {
	return []ScanRule{
		{ID: "private-key", Name: "私钥文件", Pattern: `-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`, Severity: "high", Enabled: true},
		{ID: "aws-key", Name: "AWS Access Key", Pattern: `AKIA[0-9A-Z]{16}`, Severity: "high", Enabled: true},
		{ID: "aws-secret", Name: "AWS Secret Key", Pattern: `(?i)aws_?secret_?access_?key\s*[:=]\s*['"][^'"]{20,}['"]`, Severity: "high", Enabled: true},
		{ID: "github-pat", Name: "GitHub Token", Pattern: `gh[pousr]_[A-Za-z0-9]{20,}`, Severity: "high", Enabled: true},
		{ID: "gitlab-pat", Name: "GitLab Token", Pattern: `glpat-[A-Za-z0-9_-]{20,}`, Severity: "high", Enabled: true},
		{ID: "google-api-key", Name: "Google API Key", Pattern: `AIza[0-9A-Za-z_-]{35}`, Severity: "high", Enabled: true},
		{ID: "slack-token", Name: "Slack Token", Pattern: `xox[baprs]-[0-9A-Za-z-]{10,}`, Severity: "high", Enabled: true},
		{ID: "stripe-key", Name: "Stripe Secret Key", Pattern: `sk_live_[0-9A-Za-z]{16,}`, Severity: "high", Enabled: true},
		{ID: "aliyun-ak", Name: "阿里云 AccessKey", Pattern: `LTAI[0-9A-Za-z]{12,}`, Severity: "high", Enabled: true},
		{ID: "tencent-ak", Name: "腾讯云 SecretId", Pattern: `AKID[0-9A-Za-z]{13,}`, Severity: "high", Enabled: true},
		{ID: "openai-key", Name: "OpenAI Key", Pattern: `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`, Severity: "high", Enabled: true},
		{ID: "anthropic-key", Name: "Anthropic Key", Pattern: `sk-ant-[A-Za-z0-9_-]{20,}`, Severity: "high", Enabled: true},
		{ID: "npm-token", Name: "npm Token", Pattern: `npm_[A-Za-z0-9]{30,}`, Severity: "high", Enabled: true},
		{ID: "jwt", Name: "JWT Token", Pattern: `eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`, Severity: "medium", Enabled: true},
		{ID: "bearer", Name: "Bearer Token", Pattern: `(?i)bearer\s+[A-Za-z0-9._-]{20,}`, Severity: "medium", Enabled: true},
		{ID: "basic-auth", Name: "Basic Authorization", Pattern: `(?i)authorization\s*[:=]\s*['"]?basic\s+[A-Za-z0-9+/=]{10,}`, Severity: "high", Enabled: true},
		{ID: "db-url", Name: "数据库连接串", Pattern: `(?i)(mysql|postgres|postgresql|mongodb|redis)://[^\s'"]+:[^\s'"]+@`, Severity: "high", Enabled: true},
		{ID: "jdbc-url", Name: "JDBC 连接串", Pattern: `jdbc:[a-z]+://[^\s'"]+`, Severity: "medium", Enabled: true},
		{ID: "firebase-url", Name: "Firebase 地址", Pattern: `https://[a-z0-9-]+\.firebaseio\.com`, Severity: "medium", Enabled: true},
		{ID: "wechat-appsecret", Name: "微信 AppSecret", Pattern: `(?i)app_?secret\s*[:=]\s*['"][0-9a-f]{32}['"]`, Severity: "high", Enabled: true},
		{ID: "secret-assignment", Name: "密钥赋值", Pattern: `(?i)(api[_-]?key|secret|token|password|passwd|access[_-]?key)\s*[:=]\s*['"][^'"]{8,}['"]`, Severity: "medium", Enabled: true},
		{ID: "private-token-field", Name: "私有 Token 字段", Pattern: `(?i)["']?(private[_-]?token|client[_-]?secret|refresh[_-]?token)["']?\s*[:=]\s*['"][^'"]{8,}['"]`, Severity: "medium", Enabled: true},
		{ID: "email", Name: "邮箱地址", Pattern: `[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`, Severity: "low", Enabled: true},
		{ID: "cn-id", Name: "身份证号", Pattern: `[1-9][0-9]{5}(?:19|20)[0-9]{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`, Severity: "high", Enabled: true},
		{ID: "cn-phone", Name: "手机号", Pattern: `\b1[3-9][0-9]{9}\b`, Severity: "low", Enabled: true},
		{ID: "ipv4", Name: "IPv4 地址", Pattern: `\b(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})(?:\.(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})){3}\b`, Severity: "info", Enabled: true},
	}
}

func (m *Manager) readRules() ([]ScanRule, error) {
	raw, err := os.ReadFile(m.rulesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return defaultScanRules(), nil
		}
		return nil, err
	}
	var rules []ScanRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []ScanRule{}
	}
	return rules, nil
}

func (m *Manager) writeRules(rules []ScanRule) error {
	raw, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.rulesPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.rulesPath())
}

func (m *Manager) ScanRules(args map[string]any) (any, error) {
	action := "list"
	if value, ok := args["action"].(string); ok && value != "" {
		action = value
	}
	rules, err := m.readRules()
	if err != nil {
		return nil, err
	}
	switch action {
	case "list":
		return map[string]any{"rules": rules}, nil
	case "add":
		pattern, err := requireString(args, "pattern")
		if err != nil {
			return nil, err
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("正则不合法: %w", err)
		}
		rule := ScanRule{ID: newID("rule"), Name: "custom", Pattern: pattern, Severity: "info", Enabled: true}
		if value, ok := args["name"].(string); ok && value != "" {
			rule.Name = value
		}
		if value, ok := args["severity"].(string); ok && value != "" {
			rule.Severity = value
		}
		rules = append(rules, rule)
		if err := m.writeRules(rules); err != nil {
			return nil, err
		}
		return map[string]any{"rule": rule}, nil
	case "update":
		id, err := requireString(args, "id")
		if err != nil {
			return nil, err
		}
		for i := range rules {
			if rules[i].ID != id {
				continue
			}
			if value, ok := args["name"].(string); ok && value != "" {
				rules[i].Name = value
			}
			if value, ok := args["pattern"].(string); ok && value != "" {
				if _, err := regexp.Compile(value); err != nil {
					return nil, fmt.Errorf("正则不合法: %w", err)
				}
				rules[i].Pattern = value
			}
			if value, ok := args["severity"].(string); ok && value != "" {
				rules[i].Severity = value
			}
			if value, ok := args["enabled"].(bool); ok {
				rules[i].Enabled = value
			}
			if err := m.writeRules(rules); err != nil {
				return nil, err
			}
			return map[string]any{"rule": rules[i]}, nil
		}
		return nil, fmt.Errorf("规则不存在")
	case "remove":
		id, err := requireString(args, "id")
		if err != nil {
			return nil, err
		}
		out := rules[:0]
		removed := false
		for _, rule := range rules {
			if rule.ID == id {
				removed = true
				continue
			}
			out = append(out, rule)
		}
		if !removed {
			return nil, fmt.Errorf("规则不存在")
		}
		if err := m.writeRules(out); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "id": id}, nil
	default:
		return nil, fmt.Errorf("未知 action: %s", action)
	}
}

var scanBinaryExt = map[string]bool{".wxapkg": true, ".png": true, ".jpg": true, ".zip": true, ".exe": true, ".dll": true, ".pak": true}

func secretValue(match string) string {
	i := strings.IndexAny(match, ":=")
	if i < 0 {
		return ""
	}
	value := strings.TrimSpace(match[i+1:])
	value = strings.Trim(value, `"'`)
	return value
}

func isNoiseSecret(match string) bool {
	value := secretValue(match)
	if len(value) < 8 {
		return true
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"/", "./", "../", "http://", "https://", "data:", "wx.", "#", "$", "{", "[", "("} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	switch lower {
	case "null", "undefined", "true", "false", "0", "none":
		return true
	}
	return false
}

func needsSecretValidation(rule string) bool {
	switch rule {
	case "secret-assignment", "private-token-field", "bearer", "basic-auth":
		return true
	default:
		return false
	}
}

func snippetAround(line string, start, end int) string {
	const pad = 70
	left, right := start-pad, end+pad
	if left < 0 {
		left = 0
	}
	if right > len(line) {
		right = len(line)
	}
	out := line[left:right]
	out = strings.TrimSpace(out)
	if left > 0 {
		out = "…" + out
	}
	if right < len(line) {
		out += "…"
	}
	return out
}

func severityRank(value string) int {
	switch value {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}

func displayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func (m *Manager) ScanRun(args map[string]any) (any, error) {
	root, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	targetAppID, _ := args["appid"].(string)
	targetVersion, _ := args["version"].(string)
	rules, err := m.readRules()
	if err != nil {
		return nil, err
	}
	compiled := []struct {
		rule ScanRule
		re   *regexp.Regexp
	}{}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			continue
		}
		compiled = append(compiled, struct {
			rule ScanRule
			re   *regexp.Regexp
		}{rule, re})
	}
	findings := []Finding{}
	filesScanned := 0
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.Size() > 2<<20 || scanBinaryExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		filesScanned++
		lines := strings.Split(string(raw), "\n")
		for lineNo, line := range lines {
			type candidate struct {
				ruleID, rule, severity string
				start, end             int
				match                  string
			}
			lineCandidates := []candidate{}
			for _, item := range compiled {
				locs := item.re.FindAllStringIndex(line, 20)
				for _, loc := range locs {
					match := line[loc[0]:loc[1]]
					if needsSecretValidation(item.rule.ID) && isNoiseSecret(match) {
						continue
					}
					lineCandidates = append(lineCandidates, candidate{
						ruleID: item.rule.ID, rule: item.rule.Name, severity: item.rule.Severity,
						start: loc[0], end: loc[1], match: match,
					})
				}
			}
			sort.SliceStable(lineCandidates, func(i, j int) bool {
				if severityRank(lineCandidates[i].severity) != severityRank(lineCandidates[j].severity) {
					return severityRank(lineCandidates[i].severity) < severityRank(lineCandidates[j].severity)
				}
				return lineCandidates[i].start < lineCandidates[j].start
			})
			selected := []candidate{}
			for _, current := range lineCandidates {
				overlap := false
				for _, picked := range selected {
					if current.start < picked.end && current.end > picked.start {
						overlap = true
						break
					}
				}
				if !overlap {
					selected = append(selected, current)
				}
			}
			for _, item := range selected {
				findings = append(findings, Finding{
					Rule: item.rule, Severity: item.severity,
					File: path, FileDisplay: displayPath(root, path),
					Line: lineNo + 1, Column: item.start + 1,
					Snippet: snippetAround(line, item.start, item.end),
					Match:   item.match, SourceType: "source",
				})
			}
		}
		return nil
	})
	sort.Slice(findings, func(i, j int) bool {
		order := map[string]int{"high": 0, "medium": 1, "low": 2, "info": 3}
		if order[findings[i].Severity] != order[findings[j].Severity] {
			return order[findings[i].Severity] < order[findings[j].Severity]
		}
		if findings[i].FileDisplay != findings[j].FileDisplay {
			return findings[i].FileDisplay < findings[j].FileDisplay
		}
		return findings[i].Line < findings[j].Line
	})
	report, err := m.writeReport(findings, reportMeta{
		Generated: time.Now().Format("2006-01-02 15:04:05"),
		Target:    root, AppID: targetAppID, Version: targetVersion,
		Files: filesScanned, Findings: len(findings),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"findings": findings, "report": report, "count": len(findings),
		"files_scanned": filesScanned, "target_appid": targetAppID, "target_version": targetVersion, "target_path": root}, nil
}

func (m *Manager) ScanReport() (any, error) {
	path := m.path("scan-report.html")
	st, err := os.Stat(path)
	if err != nil {
		return map[string]any{"report": "", "exists": false}, nil
	}
	return map[string]any{"report": path, "exists": true, "size": st.Size()}, nil
}

type reportMeta struct {
	Generated, Target, AppID, Version string
	Files, Findings                   int
}

const reportTemplate = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>敏感信息扫描报告</title>
<style>
:root{--bg:#f6f8f7;--panel:#fff;--text:#1f2933;--muted:#6b7280;--border:#dfe5e2;--head:#eef5f1;--accent:#16a34a;--high:#dc2626;--medium:#d97706;--low:#2563eb;--info:#6b7280}
@media(prefers-color-scheme:dark){:root{--bg:#1b1d21;--panel:#26292d;--text:#e5e7eb;--muted:#9ca3af;--border:#3a3e45;--head:#2e3136;--accent:#4ade80;--high:#f87171;--medium:#fbbf24;--low:#60a5fa;--info:#9ca3af}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:13px/1.55 "Microsoft YaHei UI",system-ui,sans-serif}
.wrap{max-width:1180px;margin:0 auto;padding:28px 22px 40px}
h1{font-size:24px;margin:0 0 4px}.sub{color:var(--muted);margin:0 0 20px}
.summary{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:10px;margin-bottom:16px}
.metric{background:var(--panel);border:1px solid var(--border);border-radius:9px;padding:12px}.metric .k{font-size:11px;color:var(--muted)}.metric .v{font-size:22px;font-weight:700;margin-top:3px}
.target{background:var(--panel);border:1px solid var(--border);border-radius:9px;padding:12px 14px;margin-bottom:16px}.target div{color:var(--muted);font-size:12px;margin:2px 0}.target code{color:var(--text)}
.toolbar{display:flex;gap:8px;align-items:center;margin:0 0 10px}.toolbar input{flex:1;max-width:340px;background:var(--panel);color:var(--text);border:1px solid var(--border);border-radius:7px;padding:8px 10px}
table{width:100%;border-collapse:collapse;background:var(--panel);border:1px solid var(--border);border-radius:9px;overflow:hidden}
th,td{padding:9px 10px;border-bottom:1px solid var(--border);text-align:left;vertical-align:top}th{background:var(--head);font-size:11px;color:var(--muted);font-weight:600}
tr:last-child td{border-bottom:0}.sev{font-weight:700}.sev.high{color:var(--high)}.sev.medium{color:var(--medium)}.sev.low{color:var(--low)}.sev.info{color:var(--info)}
.file{max-width:280px;word-break:break-all}.line{white-space:nowrap;color:var(--muted)}
code{display:block;max-width:520px;white-space:pre-wrap;word-break:break-all;background:var(--head);border-radius:5px;padding:5px 7px;font-family:Consolas,monospace;font-size:11px}
button{border:1px solid var(--border);background:var(--panel);color:var(--text);border-radius:6px;padding:4px 8px;cursor:pointer}
.empty{padding:30px;text-align:center;color:var(--muted)}
</style></head><body><div class="wrap">
<h1>敏感信息扫描报告</h1><p class="sub">WMPFWorkbench · 生成于 {{.Generated}}</p>
<div class="summary"><div class="metric"><div class="k">扫描文件</div><div class="v">{{.Files}}</div></div><div class="metric"><div class="k">命中总数</div><div class="v">{{.Findings}}</div></div><div class="metric"><div class="k">高危</div><div class="v sev high">{{.High}}</div></div><div class="metric"><div class="k">中危</div><div class="v sev medium">{{.Medium}}</div></div><div class="metric"><div class="k">低危/提示</div><div class="v">{{.LowInfo}}</div></div></div>
<div class="target"><div>目标 AppID：<code>{{if .AppID}}{{.AppID}}{{else}}未知{{end}}</code></div><div>版本：<code>{{if .Version}}{{.Version}}{{else}}未知{{end}}</code></div><div>扫描目录：<code>{{.Target}}</code></div></div>
<div class="toolbar"><input id="filter" placeholder="按规则、文件或代码片段筛选"><span class="sub" style="margin:0">共 {{.Findings}} 条</span></div>
{{if .Findings}}<table id="findings"><thead><tr><th>等级</th><th>规则</th><th>文件</th><th>位置</th><th>代码上下文 / 原始匹配</th></tr></thead><tbody>
{{range .Rows}}<tr><td><span class="sev {{.Severity}}">{{.SeverityText}}</span></td><td>{{.Rule}}</td><td class="file" title="{{.File}}">{{.FileDisplay}}</td><td class="line">{{.Line}}:{{.Column}}</td><td><code>{{.Snippet}}</code><div style="margin-top:5px"><button data-copy="{{.Match}}">复制原始内容</button></div></td></tr>{{end}}
</tbody></table>{{else}}<div class="empty">没有发现匹配项。</div>{{end}}
</div><script>
document.getElementById('filter')?.addEventListener('input',e=>{const q=e.target.value.toLowerCase();document.querySelectorAll('#findings tbody tr').forEach(tr=>{tr.style.display=tr.textContent.toLowerCase().includes(q)?'':'none';});});
document.querySelectorAll('[data-copy]').forEach(b=>b.addEventListener('click',()=>navigator.clipboard.writeText(b.dataset.copy)));
</script></body></html>`

type reportRow struct {
	Finding
	SeverityText string
}

func (m *Manager) writeReport(findings []Finding, meta reportMeta) (string, error) {
	tmpl, err := template.New("report").Parse(reportTemplate)
	if err != nil {
		return "", err
	}
	rows := make([]reportRow, 0, len(findings))
	counts := map[string]int{}
	for _, finding := range findings {
		text := map[string]string{"high": "高危", "medium": "中危", "low": "低危", "info": "提示"}[finding.Severity]
		if text == "" {
			text = finding.Severity
		}
		counts[finding.Severity]++
		rows = append(rows, reportRow{Finding: finding, SeverityText: text})
	}
	path := m.path("scan-report.html")
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return path, tmpl.Execute(file, struct {
		reportMeta
		Rows    []reportRow
		High    int
		Medium  int
		LowInfo int
	}{meta, rows, counts["high"], counts["medium"], counts["low"] + counts["info"]})
}
