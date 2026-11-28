package core

import "strings"

// LoginDiagnostic is a stable, UI-safe classification of a login failure.
// Detail is a fixed summary, never the raw error text: raw errors can carry
// process paths, PIDs or credentials and must not cross the MCP/UI boundary.
type LoginDiagnostic struct {
	Code            string
	Message         string
	ReloginRequired bool
	Detail          string
}

func ClassifyLoginError(err error) LoginDiagnostic {
	if err == nil {
		return LoginDiagnostic{Code: "ok"}
	}
	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(text, "-109") ||
		strings.Contains(lower, "ual_credentials_expired") ||
		(strings.Contains(lower, "upstream_rejected") && strings.Contains(lower, "refresh")):
		return LoginDiagnostic{
			Code:            "ual_credentials_expired",
			Message:         "UAL 凭据被上游拒绝，请重新扫码登录以刷新 access_token 与 launch_buffer。",
			ReloginRequired: true,
			Detail:          "ual_credentials_expired",
		}
	case strings.Contains(lower, "upstream_rejected"):
		return LoginDiagnostic{
			Code:            "upstream_rejected",
			Message:         "账号服务拒绝了本次检查，请稍后重试或重新扫码登录。",
			ReloginRequired: true,
			Detail:          "upstream_rejected",
		}
	case strings.Contains(lower, "method not allowed") ||
		strings.Contains(lower, "http 405") ||
		strings.Contains(lower, "x-api-protocol") ||
		(strings.Contains(lower, "无法确认") && strings.Contains(lower, "版本")) ||
		(strings.Contains(lower, "探测") && strings.Contains(lower, "无法")):
		return LoginDiagnostic{
			Code:    "sidecar_probe_inconclusive",
			Message: "无法确认授权服务版本，未自动替换进程；请检查端口占用，或更新到最新 1.0.5 后重启工作台。",
			Detail:  "HTTP 405 / probe_inconclusive",
		}
	case strings.Contains(text, "旧版 wxapkg-dump") || strings.Contains(text, "不支持账号保活"):
		return LoginDiagnostic{
			Code:    "sidecar_outdated",
			Message: "检测到旧版授权服务，工作台正在尝试替换；若仍失败请更新到最新 1.0.5 并重启。",
			Detail:  "sidecar_outdated",
		}
	case strings.Contains(lower, "session unknown") || strings.Contains(lower, "login session"):
		return LoginDiagnostic{
			Code:    "qr_session_expired",
			Message: "二维码会话已失效，请重新生成二维码。",
			Detail:  "qr_session_unknown",
		}
	case strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "connect") ||
		strings.Contains(lower, "sidecar"):
		return LoginDiagnostic{
			Code:    "sidecar_unavailable",
			Message: "授权服务不可用，请确认 wxapkg-dump 服务已启动且端口未被占用。",
			Detail:  "connection_refused",
		}
	default:
		return LoginDiagnostic{
			Code:    "login_error",
			Message: "账号检查失败，请重新生成二维码或检查授权服务。",
			Detail:  "login_error",
		}
	}
}

func LoginDiagnosticMap(err error) map[string]any {
	diag := ClassifyLoginError(err)
	out := map[string]any{
		"code":             diag.Code,
		"message":          diag.Message,
		"relogin_required": diag.ReloginRequired,
	}
	if diag.Detail != "" {
		out["detail"] = diag.Detail
	}
	if err != nil {
		// The UI-safe message replaces the raw error on purpose.
		out["error"] = diag.Message
	}
	return out
}
