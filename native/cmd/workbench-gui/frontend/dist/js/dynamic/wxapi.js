(() => {
  const d = window.WMPFDynamic;
  const c = d.common;
  const cap = d.capture;
  const esc = c.esc;
  let calls = [];
  let page = 1;
  let pageSize = 50;
  let filterText = '';
  let kind = '';
  let sort = {key: 'capturedAt', asc: false};

  function decorate(row) {
    const http = cap.httpInfo(row);
    return Object.assign({}, row, {
      _http: http,
      _method: http.method || '—',
      _host: http.host || '—',
      _path: http.isHTTP ? http.path : row.api,
      _kind: cap.kindOf(row)
    });
  }

  function filtered() {
    const q = filterText.toLowerCase();
    const rows = calls.map(decorate).filter(row => (!kind || row._kind === kind) && (!q || JSON.stringify(row).toLowerCase().includes(q)));
    return cap.sortRows(rows, sort.key, sort.asc);
  }

  function renderTable() {
    const box = document.getElementById('dynWxList');
    if (!box) return;
    const rows = filtered();
    const maxPage = Math.max(1, Math.ceil(rows.length / pageSize));
    if (page > maxPage) page = maxPage;
    const pageRows = rows.slice((page - 1) * pageSize, page * pageSize);
    const selected = new Set([...box.querySelectorAll('[data-wx-select]:checked')].map(el => el.value));
    if (!rows.length) { box.innerHTML = c.empty(calls.length ? '没有匹配的捕获记录。' : '还没有捕获到调用。'); return; }
    box.innerHTML = `<div class="dynamic-toolbar"><button data-act="wx-filter-toggle">筛选</button><button data-act="wx-delete-selected">删除选中</button><span class="dynamic-toolbar-count">共 ${calls.length} 条 · 当前 ${rows.length} 条</span></div><div class="dynamic-filter-panel" id="dynWxFilterPanel"><input class="dynamic-input" id="dynWxFilter" value="${esc(filterText)}" placeholder="筛选 Host、路径、参数或响应"><select class="dynamic-input dynamic-filter-select" id="dynWxCategory"><option value="">全部类型</option><option value="http" ${kind === 'http' ? 'selected' : ''}>HTTP 请求</option><option value="bridge" ${kind === 'bridge' ? 'selected' : ''}>原生桥</option><option value="storage" ${kind === 'storage' ? 'selected' : ''}>存储</option><option value="system" ${kind === 'system' ? 'selected' : ''}>系统/上报</option></select><button data-act="wx-filter-apply">应用筛选</button><button data-act="wx-filter-reset">重置</button></div><div class="dynamic-table-scroll"><table class="dynamic-table dynamic-capture-table"><thead><tr><th class="dynamic-col-check"><label class="dynamic-checkbox"><input type="checkbox" id="dynWxSelectAll"></label></th><th>${cap.sortHeader('时间', 'capturedAt', sort)}</th><th>${cap.sortHeader('方法', '_method', sort)}</th><th>${cap.sortHeader('Host', '_host', sort)}</th><th>${cap.sortHeader('路径 / API', '_path', sort)}</th><th>${cap.sortHeader('类型', '_kind', sort)}</th><th>${cap.sortHeader('状态', 'status', sort)}</th><th>${cap.sortHeader('码', 'statusCode', sort)}</th><th>${cap.sortHeader('请求大小', 'reqSize', sort)}</th><th>${cap.sortHeader('响应大小', 'resSize', sort)}</th><th>${cap.sortHeader('耗时', 'durationMs', sort)}</th><th>操作</th></tr></thead><tbody>${pageRows.map(row => { const st = cap.statusInfo(row); const code = cap.statusCodeOf(row); return `<tr data-wx-row="${esc(row.id)}"><td class="dynamic-col-check"><label class="dynamic-checkbox"><input type="checkbox" data-wx-select value="${esc(row.id)}" ${selected.has(row.id) ? 'checked' : ''}></label></td><td class="dynamic-nowrap">${new Date(row.capturedAt || Date.now()).toLocaleTimeString()}</td><td class="dynamic-nowrap">${esc(row._method)}</td><td class="dynamic-host-cell" title="${esc(row._host)}">${esc(row._host)}</td><td class="dynamic-api-cell" title="${esc(row._path)}">${esc(row._path)}</td><td><span class="dynamic-badge ${row._kind === 'http' ? 'on' : ''}">${esc(row._kind)}</span></td><td><span class="dynamic-badge ${st.cls}">${st.label}</span></td><td class="dynamic-nowrap">${esc(code || 'N/A')}</td><td class="dynamic-nowrap">${cap.formatSize(cap.size(row.args))}</td><td class="dynamic-nowrap">${cap.formatSize(cap.size(row.result))}</td><td class="dynamic-nowrap">${cap.duration(row)}</td><td class="dynamic-nowrap"><button data-wx-open="${esc(row.id)}">查看</button><button data-wx-remove="${esc(row.id)}">删除</button></td></tr>`; }).join('')}</tbody></table></div>${cap.pager(rows.length, page, pageSize)}`;
    const all = document.getElementById('dynWxSelectAll');
    if (all) { all.checked = pageRows.length > 0 && pageRows.every(row => selected.has(row.id)); all.addEventListener('change', () => box.querySelectorAll('[data-wx-select]').forEach(cb => { cb.checked = all.checked; })); }
    box.querySelectorAll('[data-sort]').forEach(btn => btn.addEventListener('click', () => { if (sort.key === btn.dataset.sort) sort.asc = !sort.asc; else { sort.key = btn.dataset.sort; sort.asc = true; } page = 1; renderTable(); }));
    box.querySelector('[data-act="wx-filter-toggle"]')?.addEventListener('click', () => { const panel = document.getElementById('dynWxFilterPanel'); panel.style.display = panel.style.display === 'none' || !panel.style.display ? 'flex' : 'none'; });
    box.querySelector('[data-act="wx-filter-apply"]')?.addEventListener('click', () => { filterText = document.getElementById('dynWxFilter').value; kind = document.getElementById('dynWxCategory').value; page = 1; renderTable(); });
    box.querySelector('[data-act="wx-filter-reset"]')?.addEventListener('click', () => { filterText = ''; kind = ''; page = 1; renderTable(); });
    box.querySelectorAll('[data-wx-open]').forEach(btn => btn.addEventListener('click', event => { event.stopPropagation(); openDetail(btn.dataset.wxOpen); }));
    box.querySelectorAll('[data-wx-remove]').forEach(btn => btn.addEventListener('click', async event => { event.stopPropagation(); await deleteIds([btn.dataset.wxRemove]); }));
    box.querySelectorAll('[data-wx-row]').forEach(row => row.addEventListener('click', event => { if (event.target.closest('button,input,label')) return; openDetail(row.dataset.wxRow); }));
    box.querySelector('[data-act="wx-delete-selected"]')?.addEventListener('click', async () => { await deleteIds([...box.querySelectorAll('[data-wx-select]:checked')].map(el => el.value)); });
    cap.bindPager(box, next => { page = next; renderTable(); });
  }

  function openDetail(id) {
    const call = calls.find(row => row.id === id);
    if (!call) return;
    const http = cap.httpInfo(call);
    const st = cap.statusInfo(call);
    const apiInput = document.getElementById('dynWxApi');
    const dataInput = document.getElementById('dynWxData');
    if (apiInput) apiInput.value = call.api;
    if (dataInput) dataInput.value = cap.pretty(call.args || {});
    const meta = [
      ['类型', cap.kindOf(call)], ['时间', new Date(call.capturedAt || Date.now()).toLocaleString()],
      ['状态', st.label + (st.code ? ' ' + st.code : '')], ['耗时', cap.duration(call)],
      ['请求大小', cap.formatSize(cap.size(call.args))], ['响应大小', cap.formatSize(cap.size(call.result))],
      ['记录 ID', call.id], ['原生 API', call.api]
    ];
    if (http.isHTTP) meta.unshift(['请求行', http.method + ' ' + http.path]);
    cap.openDetail({
      title: http.isHTTP ? http.method + ' ' + http.host + http.path : call.api,
      call,
      meta,
      request: call.args || {},
      requestText: http.isHTTP ? http.rawRequest : undefined,
      response: call.result == null ? (call.error || {}) : call.result,
      responseText: http.isHTTP ? cap.rawResponse(call) : undefined,
      parseRequest: http.isHTTP ? text => cap.parseRawRequest(text, call.args) : undefined,
      onSend: async data => c.invoke('wxapi.replay', {api: call.api, name: call.api, data}, null),
      formatResponse: response => {
        if (!http.isHTTP) return cap.pretty(response);
        const actual = response && response.result ? response.result : response;
        return cap.rawResponse({result: actual, statusCode: actual && actual.statusCode, responseHeaders: actual && actual.header, responseBody: actual && actual.data});
      }
    });
  }

  async function deleteIds(ids) {
    if (!ids.length) return;
    await c.invoke('wxapi.delete', {ids}, null);
    await refresh();
  }

  async function refresh() {
    const result = await c.invoke('wxapi.list', {category: ''}, null);
    calls = (result && result.calls) || [];
    renderTable();
  }

  d.register({
    key: 'wxapi', title: 'wx API',
    html: `<div class="dynamic-grid"><div class="card"><h2>捕获分类</h2><label class="dynamic-check"><input type="checkbox" id="dynWxRequest" checked> HTTP 请求</label><label class="dynamic-check"><input type="checkbox" id="dynWxStorage" checked> storage</label><label class="dynamic-check"><input type="checkbox" id="dynWxBridge" checked> native bridge</label><div class="dynamic-actions"><button data-act="wx-list">刷新列表</button></div></div><div class="card"><h2>快速重放</h2><label class="dynamic-label">API</label><input class="dynamic-input" id="dynWxApi" placeholder="wx.request 或 bridge 名称"><label class="dynamic-label">参数 JSON</label><textarea class="dynamic-textarea" id="dynWxData">{}</textarea><div class="dynamic-actions"><button data-act="wx-replay">提交重放</button><button data-act="wx-result">读取最后一次重放结果</button></div><div class="dynamic-detail" id="dynWxReplay">—</div></div></div><div class="card"><h2>捕获记录</h2><div class="dynamic-list" id="dynWxList"></div></div>`,
    init(root) {
      const sync = async () => {
        const result = await c.invoke('wxapi.capture', {request: document.getElementById('dynWxRequest').checked, storage: document.getElementById('dynWxStorage').checked, bridge: document.getElementById('dynWxBridge').checked}, 'dynWxReplay');
        if (result && result.categories) {
          document.getElementById('dynWxRequest').checked = result.categories.request;
          document.getElementById('dynWxStorage').checked = result.categories.storage;
          document.getElementById('dynWxBridge').checked = result.categories.bridge;
        }
      };
      ['dynWxRequest', 'dynWxStorage', 'dynWxBridge'].forEach(id => document.getElementById(id).addEventListener('change', sync));
      root.querySelector('[data-act="wx-list"]').addEventListener('click', refresh);
      root.querySelector('[data-act="wx-replay"]').addEventListener('click', async e => { const data = c.parseObject(document.getElementById('dynWxData').value, 'dynWxReplay'); if (data) await c.invoke('wxapi.replay', {api: document.getElementById('dynWxApi').value, data}, 'dynWxReplay', e.currentTarget); });
      root.querySelector('[data-act="wx-result"]').addEventListener('click', async e => c.out('dynWxReplay', await c.invoke('cdp_evaluate', {expression: 'window.__wmpfWxReplay || null'}, null, e.currentTarget)));
    },
    refresh
  });
})();
