(() => {
  const d = window.WMPFDynamic;
  const c = d.common;
  const esc = c.esc;
  function renderList(result) {
    const box = document.getElementById('dynScriptList');
    if (!box) return;
    const rows = (result && result.scripts) || [];
    if (!rows.length) { box.innerHTML = c.empty('还没有 Userscript。'); return; }
    box.innerHTML = rows.map(s => `<div class="dynamic-item"><div class="dynamic-item-head"><strong>${esc(s.name)}</strong><span class="dynamic-badge ${s.enabled ? 'on' : ''}">${s.enabled ? '已启用' : '已停用'}</span></div><div class="meta">${esc((s.match || []).join(', ') || '*')} · ${esc(s.runAt || 'document_start')} · ${esc(s.id)}</div><div class="dynamic-actions"><button data-script-toggle="${esc(s.id)}" data-enabled="${!s.enabled}">${s.enabled ? '停用' : '启用'}</button><button data-script-remove="${esc(s.id)}">删除</button></div></div>`).join('');
    box.querySelectorAll('[data-script-toggle]').forEach(btn => btn.addEventListener('click', async () => { await c.invoke('userscript.' + (btn.dataset.enabled === 'true' ? 'enable' : 'disable'), {id: btn.dataset.scriptToggle}); refresh(); }));
    box.querySelectorAll('[data-script-remove]').forEach(btn => btn.addEventListener('click', async () => { await c.invoke('userscript.remove', {id: btn.dataset.scriptRemove}); refresh(); }));
  }
  async function refresh() { renderList(await c.invoke('userscript.list', {}, null)); }
  d.register({
    key: 'userscript', title: 'Userscript',
    html: `<div class="dynamic-grid"><div class="card"><h2>新增脚本</h2><label class="dynamic-label">名称</label><input class="dynamic-input" id="dynScriptName" placeholder="例如：页面探针"><label class="dynamic-label">匹配 AppID（逗号分隔，空为 *）</label><input class="dynamic-input" id="dynScriptMatch" placeholder="wx123...,*"><label class="dynamic-label">运行时机</label><select class="dynamic-input" id="dynScriptRunAt"><option value="document_start">document_start</option><option value="document_end">document_end</option><option value="document_idle">document_idle</option></select><label class="dynamic-label">脚本源码</label><textarea class="dynamic-textarea" id="dynScriptSource" placeholder="// 在这里编写 JavaScript"></textarea><div class="dynamic-actions"><button data-act="script-add">保存脚本</button><button data-act="script-reload">应用到当前目标</button></div><div class="dynamic-detail" id="dynScriptOut">—</div></div><div class="card"><h2>已保存脚本</h2><p class="muted small">脚本持久化保存；目标 setup 后由后端重新注入。</p><div class="dynamic-list" id="dynScriptList"></div></div></div>`,
    init(root) {
      root.querySelector('[data-act="script-add"]').addEventListener('click', async e => {
        const name = document.getElementById('dynScriptName').value.trim();
        const source = document.getElementById('dynScriptSource').value;
        if (!name || !source.trim()) { c.out('dynScriptOut', '名称和源码不能为空'); return; }
        const match = document.getElementById('dynScriptMatch').value.split(',').map(x => x.trim()).filter(Boolean);
        await c.invoke('userscript.add', {name, source, match, runAt: document.getElementById('dynScriptRunAt').value}, 'dynScriptOut', e.currentTarget);
        refresh();
      });
      root.querySelector('[data-act="script-reload"]').addEventListener('click', async e => { await c.invoke('userscript.reload', {}, 'dynScriptOut', e.currentTarget); refresh(); });
    },
    refresh
  });
})();
