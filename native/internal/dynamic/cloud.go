package dynamic

import (
	"context"
	"fmt"
	"time"
)

const cloudAuditSource = `(() => {
  const state = window.__wmpfCloudAudit || (window.__wmpfCloudAudit = {installed:true, calls:[], seq:0});
  const seen = window.__wmpfCloudSeen || (window.__wmpfCloudSeen = new WeakSet());
  const install = w => {
    try {
      if (!w || seen.has(w)) return false;
      const cloud = w.wx && w.wx.cloud;
      if (!cloud || typeof cloud.callFunction !== 'function') return false;
      seen.add(w);
      const original = cloud.callFunction.bind(cloud);
      cloud.callFunction = function(args) {
        const id = 'cloud-' + (++state.seq) + '-' + Date.now();
        const started = Date.now();
        const record = {id:id, name:(args && args.name) || '', args:(args && args.data) || {}, capturedAt:started};
        return original(args).then(result => {
          record.result = result && result.result !== undefined ? result.result : result;
          record.error = null;
          record.durationMs = Date.now() - started;
          state.calls.unshift(record);
          if (state.calls.length > 200) state.calls.length = 200;
          return result;
        }).catch(error => {
          record.result = null;
          record.error = String(error && error.message || error);
          record.durationMs = Date.now() - started;
          state.calls.unshift(record);
          if (state.calls.length > 200) state.calls.length = 200;
          throw error;
        });
      };
      return true;
    } catch (e) { return false; }
  };
  const visit = w => {
    install(w);
    try {
      const frames = w && w.document ? w.document.querySelectorAll('iframe') : [];
      for (const frame of frames) visit(frame.contentWindow);
    } catch (e) {}
  };
  visit(window);
  return state.calls !== undefined;
})();`

func (m *Manager) CloudInstall(ctx context.Context) (any, error) {
	if _, err := m.AddScript(ctx, cloudAuditSource); err != nil {
		return nil, err
	}
	value, err := m.Eval(ctx, cloudAuditSource)
	if err != nil {
		return nil, err
	}
	return map[string]any{"installed": value == true}, nil
}

func (m *Manager) CloudList(ctx context.Context) (any, error) {
	_, _ = m.CloudInstall(ctx)
	value, err := m.Eval(ctx, "window.__wmpfCloudAudit && window.__wmpfCloudAudit.calls ? window.__wmpfCloudAudit.calls : []")
	if err != nil {
		return nil, err
	}
	return map[string]any{"calls": value}, nil
}

func (m *Manager) CloudReplay(ctx context.Context, args map[string]any) (any, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	data := objectArgs(args)
	delete(data, "name")
	expr := fmt.Sprintf(`(() => { window.__wmpfCloudReplay=null; let cloud=null; const visit=w=>{try{if(w&&w.wx&&w.wx.cloud&&typeof w.wx.cloud.callFunction==='function')cloud=w.wx.cloud;const fs=w&&w.document?w.document.querySelectorAll('iframe'):[];for(const f of fs)visit(f.contentWindow);}catch(e){}}; visit(window); if(!cloud)return false; const p=cloud.callFunction({name:%s,data:%s}); p.then(r=>{window.__wmpfCloudReplay={ok:true,result:r};}).catch(e=>{window.__wmpfCloudReplay={ok:false,error:String(e&&e.message||e)};}); return true; })()`, jsString(name), jsObject(data))
	value, err := m.Eval(ctx, expr)
	if err != nil || value != true {
		return map[string]any{"submitted": false, "name": name, "request": data}, err
	}
	for i := 0; i < 40; i++ {
		state, err := m.Eval(ctx, "window.__wmpfCloudReplay || null")
		if err == nil && state != nil {
			return map[string]any{"submitted": true, "name": name, "request": data, "response": state}, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return map[string]any{"submitted": true, "name": name, "request": data, "response": map[string]any{"ok": false, "error": "等待重放结果超时"}}, nil
}

func (m *Manager) CloudDelete(ctx context.Context, ids []string) (any, error) {
	if len(ids) == 0 {
		return map[string]any{"deleted": 0}, nil
	}
	expr := fmt.Sprintf("(function(){const ids=%s;const before=(window.__wmpfCloudAudit&&window.__wmpfCloudAudit.calls||[]).length;window.__wmpfCloudAudit.calls=(window.__wmpfCloudAudit.calls||[]).filter(c=>!ids.includes(c.id));return {before:before,remaining:window.__wmpfCloudAudit.calls.length};})()", jsObject(ids))
	value, err := m.Eval(ctx, expr)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": value}, nil
}
