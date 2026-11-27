package main

import (
	"regexp"
	"strings"
)

var diagnosticSecretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)("(?:access_token|refresh_token|token|session_key|app_session_key|auto_auth_key|login_buffer|cookie|authorization|signature|openid|uin)"\s*:\s*)"[^"]*"`), `${1}"***"`},
	{regexp.MustCompile(`(?i)((?:access_token|refresh_token|token|session_key|app_session_key|auto_auth_key|login_buffer|cookie|authorization|signature|openid|uin)\s*[=:]\s*)([^\s,&}"']+)`), `${1}***`},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._-]+`), `Bearer ***`},
	{regexp.MustCompile(`(?i)\bo[A-Za-z0-9_-]{20,}\b`), `***`},
	{regexp.MustCompile(`(?i)(guid\s*[=:]\s*)[A-Za-z0-9-]{8,}`), `${1}***`},
}

var diagnosticUserPathPlain = regexp.MustCompile(`([A-Za-z]:\\Users\\)[^\\/\s,"']+`)
var diagnosticUserPathEscaped = regexp.MustCompile(`([A-Za-z]:\\\\Users\\\\)[^\\/\s,"']+`)

// redactDiagnosticText removes account identifiers, token-shaped values and
// local filesystem paths before a diagnostic archive leaves the machine. Event
// names, error codes and timing are preserved because they carry the root-cause
// evidence without identifying the operator.
func redactDiagnosticText(text, root, temp string) string {
	for _, p := range diagnosticSecretPatterns {
		text = p.re.ReplaceAllString(text, p.repl)
	}
	for _, path := range []string{root, strings.ReplaceAll(root, `\`, `\\`)} {
		if path != "" {
			text = strings.ReplaceAll(text, path, "<APP_ROOT>")
		}
	}
	for _, path := range []string{temp, strings.ReplaceAll(temp, `\`, `\\`)} {
		if path != "" {
			text = strings.ReplaceAll(text, path, "<TEMP>")
		}
	}
	text = diagnosticUserPathPlain.ReplaceAllString(text, `${1}<USER>`)
	text = diagnosticUserPathEscaped.ReplaceAllString(text, `${1}<USER>`)
	return text
}
