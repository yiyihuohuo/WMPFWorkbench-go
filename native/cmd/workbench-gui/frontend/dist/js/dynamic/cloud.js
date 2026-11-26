(() => {
  const d = window.WMPFDynamic;
  const c = d.common;
  const cap = d.capture;
  const esc = c.esc;
  let calls = [];
  let page = 1;
  const pageSize = 50;
  let filterText = '';
  let sort = {key: 'capturedAt', asc: false};

  function filtered() {
    const q = filterText.toLowerCase();
    const rows = calls.filter(row => !q || JSON.stringify(row).toLowerCase().includes(q));
    return cap.sortRows(rows, sort.key, sort.asc);
  }

  function renderTable() {
    const box = document.getElementById('dynCloudList');
    if (!box) return;
    const rows = filtered();
    const maxPage = Math.max(1, Math.ceil(rows.length / pageSize));
    if (page > maxPage) page = maxPage;
    const pageRows = rows.slice((page - 1) * pageSize, page * pageSize);
    const selected = new Set([...box.querySelectorAll('[data-cloud-select]:checked')].map(el => el.value));
    if (!rows.length) { box.innerHTML = c.empty(calls.length ? '没有匹配的云函数调用。' : '捕获已自动安装，触发 wx.cloud.callFunction 后会出现在这里。'); return; }
    box.innerHTML = `<div class="dynamic-toolbar"><button data-act="cloud-filter-toggle">筛选</button><button data-act="cloud-delete-selected">删除选中</button><span class="dynamic-toolbar-count">共 ${calls.length} 条 · 当前 ${rows.length} 条</span></div><div class="dynamic-filter-panel" id="dynCloudFilterPanel"><input class="dynamic-input" id="dynCloudFilter" value="${esc(filterText)}" placeholder="筛选函数名、参数或返回"><button data-act="cloud-filter-apply">应用筛选</button><button data-act="cloud-filter-reset">重置</button></div><div class="dynamic-table-scroll"><table class="dynamic-table dynamic-capture-table"><thead><tr><th class="dynamic-col-check"><label class="dynamic-checkbox"><input type="checkbox" id="dynCloudSelectAll"></label></th><th>${cap.sortHeader('时间', 'capturedAt', sort)}</th><th>${cap.sortHeader('函数', 'name', sort)}</th><th>${cap.sortHeader('状态', 'status', sort)}</th><th>${cap.sortHeader('请求大小', 'reqSize', sort)}</th><th>${cap.sortHeader('响应大小', 'resSize', sort)}</th><th>${cap.sortHeader('耗时', 'durationMs', sort)}</th><th>操作</th></tr></thead><tbody>${pageRows.map(row => { const st = cap.statusInfo(row); return `<tr data-cloud-row="${esc(row.id)}"><td class="dynamic-col-check"><label class="dynamic-checkbox"><input type="checkbox" data-cloud-select value="${esc(row.id)}" ${selected.has(row.id) ? 'checked' : ''}></label></td><td class="dynamic-nowrap">${new Date(row.capturedAt || Date.now()).toLocaleTimeString()}</td><td class="dynamic-api-cell" title="${esc(row.name)}">${esc(row.name)}</td><td><span class="dynamic-badge ${st.cls}">${st.label}</span></td><td class="dynamic-nowrap">${cap.formatSize(cap.size(row.args))}</td><td class="dynamic-nowrap">${cap.formatSize(cap.size(row.result))}</td><td class="dynamic-nowrap">${cap.duration(row)}</td><td class="dynamic-nowrap"><button data-cloud-open="${esc(row.id)}">查看</button><button data-cloud-remove="${esc(row.id)}">删除</button></td></tr>`; }).join('')}</tbody></table></div>${cap.pager(rows.length, page, pageSize)}`;
    const all = document.getElementById('dynCloudSelectAll');
    if (all) { all.checked = pageRows.length > 0 && pageRows.every(row => selected.has(row.id)); all.addEventListener('change', () => box.querySelectorAll('[data-cloud-select]').forEach(cb => { cb.checked = all.checked; })); }
    box.querySelectorAll('[data-sort]').forEach(btn => btn.addEventListener('click', () => { if (sort.key === btn.dataset.sort) sort.asc = !sort.asc; else { sort.key = btn.dataset.sort; sort.asc = true; } page = 1; renderTable(); }));
    box.querySelector('[data-act="cloud-filter-toggle"]')?.addEventListener('click', () => { const panel = document.getElementById('dynCloudFilterPanel'); panel.style.display = panel.style.display === 'none' || !panel.style.display ? 'flex' : 'none'; });
    box.querySelector('[data-act="cloud-filter-apply"]')?.addEventListener('click', () => { filterText = document.getElementById('dynCloudFilter').value; page = 1; renderTable(); });
    box.querySelector('[data-act="cloud-filter-reset"]')?.addEventListener('click', () => { filterText = ''; page = 1; renderTable(); });
    box.querySelectorAll('[data-cloud-open]').forEach(btn => btn.addEventListener('click', event => { event.stopPropagation(); openDetail(btn.dataset.cloudOpen); }));
    box.querySelectorAll('[data-cloud-remove]').forEach(btn => btn.addEventListener('click', async event => { event.stopPropagation(); await deleteIds([btn.dataset.cloudRemove]); }));
    box.querySelectorAll('[data-cloud-row]').forEach(row => row.addEventListener('click', event => { if (event.target.closest('button,input,label')) return; openDetail(row.dataset.cloudRow); }));
    box.querySelector('[data-act="cloud-delete-selected"]')?.addEventListener('click', async () => { await deleteIds([...box.querySelectorAll('[data-cloud-select]:checked')].map(el => el.value)); });
    cap.bindPager(box, next => { page = next; renderTable(); });
  }

  function openDetail(id) {
    const call = calls.find(row => row.id === id);
    if (!call) return;
    const st = cap.statusInfo(call);
    const nameInput = document.getElementById('dynCloudName');
    const dataInput = document.getElementById('dynCloudData');
    if (nameInput) nameInput.value = call.name || '';
    if (dataInput) dataInput.value = cap.pretty(call.args || {});
    cap.openDetail({
      title: call.name || '云函数',
      call,
      meta: [['函数', call.name || '—'], ['时间', new Date(call.capturedAt || Date.now()).toLocaleString()], ['状态', st.label], ['耗时', cap.duration(call)], ['请求大小', cap.formatSize(cap.size(call.args))], ['响应大小', cap.formatSize(cap.size(call.result))], ['记录 ID', call.id]],
      request: call.args || {},
      response: call.result == null ? (call.error || {}) : call.result,
      onSend: async data => c.invoke('cloud_capture.replay', {name: call.name, data}, null)
    });
  }

  async function deleteIds(ids) {
    if (!ids.length) return;
    await c.invoke('cloud_capture.delete', {ids}, null);
    await refresh();
  }

  async function refresh() {
    renderTable();
    const result = await c.invoke('cloud_capture.list', {}, null);
    calls = (result && result.calls) || [];
    renderTable();
  }

  d.register({
    key: 'cloud', title: '云函数',
    html: `<div class="dynamic-grid"><div class="card"><h2>运行期捕获</h2><p class="muted small">默认开启，捕获 wx.cloud.callFunction 的真实参数和结果。</p><div class="dynamic-actions"><button data-act="cloud-list">刷新列表</button><button data-act="cloud-clear">清空本地显示</button></div><div class="dynamic-detail" id="dynCloudReplay">—</div></div><div class="card"><h2>快速重放</h2><label class="dynamic-label">函数名</label><input class="dynamic-input" id="dynCloudName" placeholder="functionName"><label class="dynamic-label">参数 JSON</label><textarea class="dynamic-textarea" id="dynCloudData">{}</textarea><div class="dynamic-actions"><button data-act="cloud-replay">提交重放</button><button data-act="cloud-result">读取最后一次重放结果</button></div></div></div><div class="card"><h2>捕获记录</h2><div class="dynamic-list" id="dynCloudList"></div></div>`,
    init(root) {
      root.querySelector('[data-act="cloud-list"]').addEventListener('click', refresh);
      root.querySelector('[data-act="cloud-clear"]').addEventListener('click', () => { calls = []; renderTable(); });
      root.querySelector('[data-act="cloud-replay"]').addEventListener('click', async e => { const data = c.parseObject(document.getElementById('dynCloudData').value, 'dynCloudReplay'); if (data) await c.invoke('cloud_capture.replay', {name: document.getElementById('dynCloudName').value, data}, 'dynCloudReplay', e.currentTarget); });
      root.querySelector('[data-act="cloud-result"]').addEventListener('click', async e => { const r = await c.invoke('cdp_evaluate', {expression: 'window.__wmpfCloudReplay || null'}, 'dynCloudReplay', e.currentTarget); if (r) c.out('dynCloudReplay', r); });
    },
    refresh
  });
})();
