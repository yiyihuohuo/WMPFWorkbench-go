package dynamic

import (
	"context"
	"fmt"
	"time"
)

const wxAPISource = `(() => {
  const state = window.__wmpfWxAudit || (window.__wmpfWxAudit = {installed:true, version:2, userConfigured:false, categories:{request:true, storage:true, bridge:true}, calls:[], seq:0});
  if (!state.version || !state.userConfigured) {
    state.version = 2;
    state.categories = {request:true, storage:true, bridge:true};
  }
  const seen = window.__wmpfWxSeen || (window.__wmpfWxSeen = new WeakSet());
  const record = (category, api, args, result, error, durationMs) => {
    if (!state.categories[category]) return;
    state.calls.unshift({id:'wx-'+(++state.seq)+'-'+Date.now(), category:category, api:api, args:args||{}, result:result||null, error:error||null, durationMs:durationMs==null?null:durationMs, capturedAt:Date.now()});
    if (state.calls.length > 200) state.calls.length = 200;
  };
  const install = w => {
    try {
      if (!w || seen.has(w)) return false;
      const wx = w.wx;
      const bridge = w.WeixinJSBridge;
      if (!wx && !bridge) return false;
      seen.add(w);
      if (wx && typeof wx.request === 'function') {
        const original = wx.request.bind(wx);
        wx.request = function(args) {
          const started = Date.now();
          const wrapped = Object.assign({}, args, {success:function(res){record('request','wx.request',args,res,null,Date.now()-started); if(args&&args.success)args.success(res);}, fail:function(err){record('request','wx.request',args,null,String(err),Date.now()-started); if(args&&args.fail)args.fail(err);}});
          return original(wrapped);
        };
      }
      if (wx) {
        for (const name of ['getStorage','setStorage','removeStorage','clearStorage']) {
          if (typeof wx[name] !== 'function') continue;
          const original = wx[name].bind(wx);
          wx[name] = function(args) { const started = Date.now(); const result = original(args); record('storage','wx.'+name,args,result,null,Date.now()-started); return result; };
        }
      }
      const enrichRequestResult = result => {
        if (!result || typeof result !== 'object') return;
        const taskId = result.requestTaskId;
        if (taskId == null) return;
        const call = state.calls.find(c => c.result && c.result.requestTaskId === taskId);
        if (!call) return;
        call.statusCode = result.statusCode || result.status || call.statusCode;
        call.responseHeaders = result.header || result.headers || call.responseHeaders;
        call.responseBody = result.data !== undefined ? result.data : call.responseBody;
        call.completedAt = Date.now();
        call.durationMs = call.completedAt - call.capturedAt;
        call.result = Object.assign({}, call.result || {}, result);
        if (window.__wmpfWxReplayTaskId != null && result.requestTaskId === window.__wmpfWxReplayTaskId) {
          window.__wmpfWxReplay = {ok:true, result:result};
        }
      };
      if (bridge && typeof bridge.invoke === 'function') {
        const original = bridge.invoke.bind(bridge);
        bridge.invoke = function(name, args, callback) {
          const started = Date.now();
          return original(name, args, function(result){ record('bridge',name,args,result,null,Date.now()-started); enrichRequestResult(result); if(callback)callback(result); });
        };
      }
      if (bridge && typeof bridge.on === 'function') {
        const originalOn = bridge.on.bind(bridge);
        bridge.on = function(name, callback) {
          if (name === 'onRequestTaskStateChange') {
            const wrapped = function(result) { enrichRequestResult(result); if (callback) return callback(result); };
            return originalOn(name, wrapped);
          }
          return originalOn(name, callback);
        };
      }
      if (bridge && typeof bridge.subscribe === 'function') {
        const originalSubscribe = bridge.subscribe.bind(bridge);
        bridge.subscribe = function(name, callback) {
          if (name === 'onRequestTaskStateChange') {
            const wrapped = function(result) { enrichRequestResult(result); if (callback) return callback(result); };
            return originalSubscribe(name, wrapped);
          }
          return originalSubscribe(name, callback);
        };
      }
      if (bridge && typeof bridge.on === 'function') {
        try { bridge.on('onRequestTaskStateChange', function(result) { enrichRequestResult(result); }); } catch (e) {}
      }
      if (bridge && typeof bridge.subscribe === 'function') {
        try { bridge.subscribe('onRequestTaskStateChange', function(result) { enrichRequestResult(result); }); } catch (e) {}
      }
      return true;
    } catch (e) { return false; }
  };
  const visit = w => { install(w); try { const fs=w&&w.document?w.document.querySelectorAll('iframe'):[]; for(const f of fs)visit(f.contentWindow); } catch(e){} };
  visit(window);
  return true;
})();`

func (m *Manager) WxapiCapture(ctx context.Context, args map[string]any) (any, error) {
	if _, err := m.AddScript(ctx, wxAPISource); err != nil {
		return nil, err
	}
	if _, err := m.Eval(ctx, wxAPISource); err != nil {
		return nil, err
	}
	expr := "(function(){const c=window.__wmpfWxAudit.categories;"
	hasSetting := false
	for _, key := range []string{"request", "storage", "bridge"} {
		if raw, ok := args[key]; ok {
			value, _ := raw.(bool)
			expr += fmt.Sprintf("c[%s]=%t;", jsString(key), value)
			hasSetting = true
		}
	}
	if hasSetting {
		expr += "window.__wmpfWxAudit.userConfigured=true;"
	}
	expr += "return c;})()"
	value, err := m.Eval(ctx, expr)
	if err != nil {
		return nil, err
	}
	return map[string]any{"categories": value}, nil
}

func (m *Manager) WxapiList(ctx context.Context, category string) (any, error) {
	if _, err := m.AddScript(ctx, wxAPISource); err == nil {
		_, _ = m.Eval(ctx, wxAPISource)
	}
	expr := "window.__wmpfWxAudit && window.__wmpfWxAudit.calls ? window.__wmpfWxAudit.calls : []"
	if category != "" {
		expr = fmt.Sprintf("(window.__wmpfWxAudit && window.__wmpfWxAudit.calls || []).filter(c=>c.category===%s)", jsString(category))
	}
	value, err := m.Eval(ctx, expr)
	if err != nil {
		return nil, err
	}
	return map[string]any{"calls": value}, nil
}

func (m *Manager) WxapiReplay(ctx context.Context, args map[string]any) (any, error) {
	api, err := requireString(args, "api")
	if err != nil {
		return nil, err
	}
	data := objectArgs(args)
	delete(data, "api")
	expr := ""
	if api == "wx.request" || api == "request" {
		expr = fmt.Sprintf("(() => { window.__wmpfWxReplay=null; let wx=null; const visit=w=>{try{if(w&&w.wx&&typeof w.wx.request==='function')wx=w.wx;const fs=w&&w.document?w.document.querySelectorAll('iframe'):[];for(const f of fs)visit(f.contentWindow);}catch(e){}}; visit(window); if(!wx)return false; wx.request(Object.assign(%s,{success:r=>window.__wmpfWxReplay={ok:true,result:r},fail:e=>window.__wmpfWxReplay={ok:false,error:String(e&&e.errMsg||e)}}));return true; })()", jsObject(data))
	} else if api == "createRequestTask" {
		expr = fmt.Sprintf("(() => { window.__wmpfWxReplay=null; window.__wmpfWxReplayTaskId=null; let bridge=null; const visit=w=>{try{if(w&&w.WeixinJSBridge&&typeof w.WeixinJSBridge.invoke==='function')bridge=w.WeixinJSBridge;const fs=w&&w.document?w.document.querySelectorAll('iframe'):[];for(const f of fs)visit(f.contentWindow);}catch(e){}}; visit(window); if(!bridge)return false; bridge.invoke('createRequestTask',%s,r=>{if(r&&r.requestTaskId!=null){window.__wmpfWxReplayTaskId=r.requestTaskId;}else{window.__wmpfWxReplay={ok:true,result:r};}});return true; })()", jsObject(data))
	} else {
		name, _ := args["name"].(string)
		if name == "" {
			name = api
		}
		expr = fmt.Sprintf("(() => { window.__wmpfWxReplay=null; let bridge=null; const visit=w=>{try{if(w&&w.WeixinJSBridge&&typeof w.WeixinJSBridge.invoke==='function')bridge=w.WeixinJSBridge;const fs=w&&w.document?w.document.querySelectorAll('iframe'):[];for(const f of fs)visit(f.contentWindow);}catch(e){}}; visit(window); if(!bridge)return false; bridge.invoke(%s,%s,r=>window.__wmpfWxReplay={ok:true,result:r});return true; })()", jsString(name), jsObject(data))
	}
	value, err := m.Eval(ctx, expr)
	if err != nil || value != true {
		return map[string]any{"submitted": false, "api": api, "request": data}, err
	}
	for i := 0; i < 40; i++ {
		state, err := m.Eval(ctx, "window.__wmpfWxReplay || null")
		if err == nil && state != nil {
			return map[string]any{"submitted": true, "api": api, "request": data, "response": state}, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return map[string]any{"submitted": true, "api": api, "request": data, "response": map[string]any{"ok": false, "error": "等待重放结果超时"}}, nil
}

func (m *Manager) WxapiDelete(ctx context.Context, ids []string) (any, error) {
	if len(ids) == 0 {
		return map[string]any{"deleted": 0}, nil
	}
	expr := fmt.Sprintf("(function(){const ids=%s;const before=(window.__wmpfWxAudit&&window.__wmpfWxAudit.calls||[]).length;window.__wmpfWxAudit.calls=(window.__wmpfWxAudit.calls||[]).filter(c=>!ids.includes(c.id));return {before:before,remaining:window.__wmpfWxAudit.calls.length};})()", jsObject(ids))
	value, err := m.Eval(ctx, expr)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": value}, nil
}
