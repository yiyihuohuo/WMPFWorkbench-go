package main

import (
	"regexp"
	"strings"
)

// Keys are matched case-insensitively and with an optional underscore, so
// snake_case, camelCase and PascalCase spellings are all covered.
const diagnosticKeyAlternation = `signature|sign|access_?token|refresh_?token|token|session_?key|app_?session_?key|auto_?auth_?key|login_?buffer|union_?id|open_?id|session_?id|uin|cookie|authorization`

var diagnosticSecretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// A Cookie/Set-Cookie header can hold many key=value pairs on one line.
	{regexp.MustCompile(`(?i)(\bset-cookie\s*:\s*)([^\r\n]*)`), `${1}***`},
	{regexp.MustCompile(`(?i)(\bcookie\s*[=:]\s*)([^\r\n"']*)`), `${1}***`},
	{regexp.MustCompile(`(?is)("cookie"\s*:\s*)\{[^{}]*\}`), `${1}{"***":"***"}`},
	{regexp.MustCompile(`(?i)("(?:` + diagnosticKeyAlternation + `)"\s*:\s*)"[^"]*"`), `${1}"***"`},
	{regexp.MustCompile(`(?i)([{(,]\s*(?:` + diagnosticKeyAlternation + `)\s*:\s*)"[^"]*"`), `${1}"***"`},
	{regexp.MustCompile(`(?i)(\b(?:` + diagnosticKeyAlternation + `)\s*[=:]\s*)([^\s,&}"';]+)`), `${1}***`},
	{regexp.MustCompile(`(?i)([{(,]\s*(?:` + diagnosticKeyAlternation + `)\s*:\s*)[^,}\s]+`), `${1}***`},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._-]+`), `Bearer ***`},
	{regexp.MustCompile(`(?i)\bo[A-Za-z0-9_-]{20,}\b`), `***`},
	{regexp.MustCompile(`(?i)(\bguid\s*[=:]\s*)[A-Za-z0-9-]{8,}`), `${1}***`},
}

var diagnosticUserPathPlain = regexp.MustCompile(`(?i)([A-Za-z]:\\Users\\)[^\\/\s,"']+`)
var diagnosticUserPathEscaped = regexp.MustCompile(`(?i)([A-Za-z]:\\\\Users\\\\)[^\\/\s,"']+`)

// redactDiagnosticPath removes local path variants without depending on the
// case used by the producer (Windows paths are case-insensitive on disk).
func redactDiagnosticPath(text, path, marker string) string {
	if path == "" {
		return text
	}
	for _, variant := range []string{path, strings.ReplaceAll(path, `\`, `\\`)} {
		if variant == "" {
			continue
		}
		re, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(variant))
		if err != nil {
			continue
		}
		text = re.ReplaceAllString(text, marker)
	}
	return text
}

// redactDiagnosticText removes account identifiers, token-shaped values and
// local filesystem paths before a diagnostic archive leaves the machine. Event
// names, error codes and timing are preserved because they carry the root-cause
// evidence without identifying the operator.
func redactDiagnosticText(text, root, temp string) string {
	for _, p := range diagnosticSecretPatterns {
		text = p.re.ReplaceAllString(text, p.repl)
	}
	text = redactDiagnosticPath(text, root, "<APP_ROOT>")
	text = redactDiagnosticPath(text, temp, "<TEMP>")
	text = diagnosticUserPathPlain.ReplaceAllString(text, `${1}<USER>`)
	text = diagnosticUserPathEscaped.ReplaceAllString(text, `${1}<USER>`)
	return text
}
