package dynamic

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const routeGuardSource = `(() => {
  const state = window.__wmpfRouteGuard || (window.__wmpfRouteGuard = {installed:true, enabled:false, blocked:[]});
  const seen = window.__wmpfRouteGuardSeen || (window.__wmpfRouteGuardSeen = new WeakSet());
  const install = w => {
    try {
      if (!w || !w.wx || seen.has(w.wx)) return false;
      const wx = w.wx;
      seen.add(wx);
      for (const name of ['navigateTo','redirectTo','reLaunch','switchTab']) {
        if (typeof wx[name] !== 'function') continue;
        const original = wx[name].bind(wx);
        wx[name] = function(args) {
          if (state.enabled) {
            state.blocked.push({api:name,args:args||{},at:Date.now()});
            if (state.blocked.length > 200) state.blocked.length = 200;
            if (args && typeof args.fail === 'function') args.fail({errMsg:name+':fail blocked by WMPFWorkbench'});
            return Promise.resolve({errMsg:name+':fail blocked by WMPFWorkbench'});
          }
          return original(args);
        };
      }
      return true;
    } catch (e) { return false; }
  };
  const visit = w => { install(w); try { const fs=w&&w.document?w.document.querySelectorAll('iframe'):[]; for(const f of fs)visit(f.contentWindow); } catch(e){} };
  visit(window);
  return true;
})();`

var routeCategoryPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"auth", regexp.MustCompile(`(?i)(login|auth|register|sign)`)},
	{"payment", regexp.MustCompile(`(?i)(pay|payment|wallet|refund)`)},
	{"order", regexp.MustCompile(`(?i)(order|trade|cart|checkout)`)},
	{"detail", regexp.MustCompile(`(?i)(detail|info|article|goods)`)},
	{"user", regexp.MustCompile(`(?i)(user|profile|account|mine)`)},
	{"setting", regexp.MustCompile(`(?i)(setting|config|about|help)`)},
	{"form", regexp.MustCompile(`(?i)(form|edit|submit|create)`)},
	{"list", regexp.MustCompile(`(?i)(list|search|result|catalog)`)},
	{"home", regexp.MustCompile(`(?i)(index|home|main)`)},
}

func classifyRoute(path string) string {
	for _, item := range routeCategoryPatterns {
		if item.re.MatchString(path) {
			return item.name
		}
	}
	return "other"
}

const routeListExpr = `(() => {
  const results = [];
  const visit = w => {
    try {
      const cfg = w && w.__wxConfig;
      if (cfg) {
        const appid = cfg.accountInfo && cfg.accountInfo.appId || cfg.appid || '';
        const add = path => { if (path && typeof path === 'string') results.push({appid:appid, path:path, category:''}); };
        (cfg.pages || []).forEach(add);
        (cfg.subPackages || cfg.subpackages || []).forEach(p => {
          const root = (p && p.root || '').replace(/\/$/, '');
          (p && p.pages || []).forEach(page => add(root + '/' + page));
        });
      }
    } catch (e) {}
    try {
      const frames = w && w.document ? w.document.querySelectorAll('iframe') : [];
      for (const frame of frames) visit(frame.contentWindow);
    } catch (e) {}
  };
  visit(window);
  const unique = {};
  results.forEach(r => unique[r.path] = r);
  return Object.keys(unique).sort().map(k => unique[k]);
})();`

func (m *Manager) RouteList(ctx context.Context) (any, error) {
	value, err := m.Eval(ctx, routeListExpr)
	if err != nil {
		return nil, err
	}
	rows, _ := value.([]any)
	out := []any{}
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := row["path"].(string)
		row["category"] = classifyRoute(path)
		out = append(out, row)
	}
	return map[string]any{"routes": out}, nil
}

func (m *Manager) RouteNavigate(ctx context.Context, args map[string]any) (any, error) {
	path, err := requireString(args, "path")
	if err != nil {
		return nil, err
	}
	path = normalizeRoutePath(path)
	method, _ := args["method"].(string)
	methods := []string{"reLaunch", "switchTab", "redirectTo", "navigateTo"}
	if method != "" {
		methods = []string{method}
	}
	expr := fmt.Sprintf(`(() => { window.__wmpfRouteNavigate=null; let wx=null; const visit=w=>{try{if(w&&w.wx)wx=w.wx;const fs=w&&w.document?w.document.querySelectorAll('iframe'):[];for(const f of fs)visit(f.contentWindow);}catch(e){}}; visit(window); if(!wx){window.__wmpfRouteNavigate={ok:false,error:'wx context not found'};return false;} const path=%s;const methods=%s;let i=0;const next=()=>{if(i>=methods.length){window.__wmpfRouteNavigate={ok:false,error:'all navigation methods failed'};return;}const m=methods[i++];try{const r=wx[m]({url:path,success:r=>window.__wmpfRouteNavigate={ok:true,method:m,result:r||null},fail:e=>{if(i<methods.length){next();}else{window.__wmpfRouteNavigate={ok:false,method:m,error:(e&&e.errMsg)||String(e)};}}});if(r&&r.catch)r.catch(next);}catch(e){next();}};next();return true; })()`, jsString(path), jsObject(methods))
	value, err := m.Eval(ctx, expr)
	if err != nil || value != true {
		return map[string]any{"submitted": false, "path": path, "methods": methods}, err
	}
	for i := 0; i < 40; i++ {
		state, err := m.Eval(ctx, "window.__wmpfRouteNavigate || null")
		if err == nil && state != nil {
			return map[string]any{"submitted": true, "path": path, "methods": methods, "navigation": state}, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return map[string]any{"submitted": true, "path": path, "methods": methods, "navigation": map[string]any{"ok": false, "error": "等待导航结果超时"}}, nil
}

func (m *Manager) RouteGuard(ctx context.Context, enabled bool) (any, error) {
	if _, err := m.AddScript(ctx, routeGuardSource); err != nil {
		return nil, err
	}
	if _, err := m.Eval(ctx, routeGuardSource); err != nil {
		return nil, err
	}
	expr := fmt.Sprintf("(function(){window.__wmpfRouteGuard.enabled=%t;return window.__wmpfRouteGuard.enabled;})()", enabled)
	value, err := m.Eval(ctx, expr)
	return map[string]any{"enabled": value == true}, err
}

func normalizeRoutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}
