package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var wxAppIDPattern = regexp.MustCompile(`^wx[0-9a-f]{16}$`)

type jumpArgs struct {
	TargetAppID     string
	SourceHandler   string
	ButtonSelector  string
	SourceX         float64
	SourceY         float64
	ConfirmSelector string
	TimeoutMS       int
	AutoConfirm     bool
}

func validateJumpArgs(args map[string]any) (jumpArgs, error) {
	out := jumpArgs{}
	out.TargetAppID, _ = args["target_appid"].(string)
	if !wxAppIDPattern.MatchString(out.TargetAppID) {
		return out, errors.New("target_appid must match wx + 16 lowercase hex characters")
	}
	if raw, ok := args["source_handler"]; ok {
		out.SourceHandler, _ = raw.(string)
		if out.SourceHandler == "" {
			return out, errors.New("source_handler cannot be empty")
		}
	} else {
		out.SourceHandler = "goYY"
	}
	if !identifierPattern.MatchString(out.SourceHandler) {
		return out, errors.New("source_handler must be a plain JavaScript identifier")
	}
	if raw, ok := args["button_selector"]; ok {
		out.ButtonSelector, _ = raw.(string)
		if out.ButtonSelector == "" {
			return out, errors.New("button_selector cannot be empty")
		}
	} else {
		out.ButtonSelector = ".yy-btn"
	}
	if raw, ok := args["confirm_selector"]; ok {
		out.ConfirmSelector, _ = raw.(string)
		if out.ConfirmSelector == "" {
			return out, errors.New("confirm_selector cannot be empty")
		}
	} else {
		out.ConfirmSelector = "button.btn_allow"
	}
	out.SourceX, out.SourceY = 70, 260
	if raw, ok := args["source_x"]; ok {
		switch v := raw.(type) {
		case float64:
			out.SourceX = v
		case int:
			out.SourceX = float64(v)
		default:
			return out, errors.New("source_x must be a number")
		}
	}
	if raw, ok := args["source_y"]; ok {
		switch v := raw.(type) {
		case float64:
			out.SourceY = v
		case int:
			out.SourceY = float64(v)
		default:
			return out, errors.New("source_y must be a number")
		}
	}
	out.TimeoutMS = 20000
	if raw, ok := args["timeout_ms"]; ok {
		switch v := raw.(type) {
		case float64:
			out.TimeoutMS = int(v)
		case int:
			out.TimeoutMS = v
		case int64:
			out.TimeoutMS = int(v)
		default:
			return out, errors.New("timeout_ms must be an integer")
		}
	}
	if out.TimeoutMS < 1000 || out.TimeoutMS > 120000 {
		return out, errors.New("timeout_ms must be between 1000 and 120000")
	}
	out.AutoConfirm = true
	if v, ok := args["auto_confirm"].(bool); ok {
		out.AutoConfirm = v
	}
	return out, nil
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func jsString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func buildInjectJumpScript(args jumpArgs) string {
	target := jsString(args.TargetAppID)
	handler := jsString(args.SourceHandler)
	return fmt.Sprintf(`(() => {
  const target = %s;
  const handler = %s;
  const frame = document.querySelector('iframe[name=appContext]');
  if (!frame || !frame.contentWindow) return {ok:false, stage:'inject', error:'appContext iframe missing'};
  const w = frame.contentWindow;
  const pages = typeof w.getCurrentPages === 'function' ? w.getCurrentPages() : [];
  const page = pages[pages.length - 1];
  if (!page) return {ok:false, stage:'inject', error:'no current page'};
  if (!page.__wmpfOriginalJumpHandler) page.__wmpfOriginalJumpHandler = page[handler];
  if (typeof page[handler] !== 'function') return {ok:false, stage:'inject', error:'source handler missing', handler};
  page[handler] = function () {
    const api = w.wx || w.WeixinJSBridge;
    if (!api || typeof api.navigateToMiniProgram !== 'function') {
      w.__wmpfJumpEvidence = {target, state:'fail', errMsg:'navigateToMiniProgram unavailable'};
      return undefined;
    }
    w.__wmpfJumpEvidence = {target, state:'invoked'};
    return api.navigateToMiniProgram({
      appId: target,
      envVersion: 'release',
      success(r) { w.__wmpfJumpEvidence = {target, state:'success', errMsg:(r && r.errMsg) || 'navigateToMiniProgram:ok'}; },
      fail(r) { w.__wmpfJumpEvidence = {target, state:'fail', errMsg:r && r.errMsg || 'navigateToMiniProgram:fail'}; },
      complete(r) { w.__wmpfJumpEvidence.complete = (r && r.errMsg) || 'complete'; }
    });
  };
  return {ok:true, stage:'inject', handler, route:page.route || '', target};
})()`, target, handler)
}

func buildRectScript(buttonSelector, confirmSelector string) string {
	return fmt.Sprintf(`(() => {
  const buttonSelector = %s;
  const confirmSelector = %s;
  const visible = e => { if (!e) return false; const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  const center = e => { const r = e.getBoundingClientRect(); return {x:r.left + r.width/2, y:r.top + r.height/2, w:r.width, h:r.height}; };
  const frame = document.querySelector('iframe[name=appContext]');
  const w = frame && frame.contentWindow;
  let source = null;
  if (w && w.document) {
    source = w.document.querySelector(buttonSelector);
    if (visible(source)) {
      const ir = frame.getBoundingClientRect();
      const sr = center(source);
      source = {x: sr.x + ir.left, y: sr.y + ir.top, w: sr.w, h: sr.h, selector: buttonSelector};
    } else source = null;
  }
  let confirm = document.querySelector(confirmSelector);
  if (!visible(confirm)) {
    const candidates = Array.from(document.querySelectorAll('button, view, div, span'));
    confirm = candidates.find(e => /允许|确认|打开|确定/.test((e.textContent || '').trim())) || null;
  }
  const result = {viewport:{w:innerWidth, h:innerHeight}, source, confirm:null};
  if (visible(confirm)) result.confirm = Object.assign(center(confirm), {selector: confirmSelector});
  return result;
})()`, jsString(buttonSelector), jsString(confirmSelector))
}

func jumpTimeout(args jumpArgs) time.Duration {
	return time.Duration(args.TimeoutMS) * time.Millisecond
}
