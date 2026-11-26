(() => {
  const d = window.WMPFDynamic;
  if (!d) return;
  const c = d.common;
  const esc = c.esc;
  const enc = new TextEncoder();

  function pretty(value) {
    try { return JSON.stringify(typeof value === 'string' ? JSON.parse(value) : value, null, 2); }
    catch (e) { return String(value == null ? '' : value); }
  }
  function highlight(value) {
    return esc(pretty(value))
      .replace(/(&quot;(?:\\.|[^&])*?&quot;)(\s*:)/g, '<span class="json-key">$1</span>$2')
      .replace(/:\s*(&quot;.*?&quot;)/g, ': <span class="json-string">$1</span>')
      .replace(/\b(true|false|null)\b/g, '<span class="json-bool">$1</span>')
      .replace(/\b(-?\d+(?:\.\d+)?)\b/g, '<span class="json-num">$1</span>');
  }
  function size(value) {
    if (value == null) return 0;
    try { return enc.encode(typeof value === 'string' ? value : JSON.stringify(value)).length; }
    catch (e) { return 0; }
  }
  function formatSize(bytes) {
    if (bytes == null || bytes < 0) return '—';
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / 1024 / 1024).toFixed(1) + ' MB';
  }
  function statusCodeOf(call) {
    if (!call) return '';
    const roots = [call, call.result, call.response, call.data, call.result && call.result.data, call.result && call.result.result, call.result && call.result.response, call.result && call.result.res];
    for (const root of roots) {
      if (!root || typeof root !== 'object') continue;
      for (const key of ['statusCode', 'status_code', 'httpStatusCode', 'http_status_code', 'code']) {
        const value = root[key];
        if (typeof value === 'number' && value >= 100 && value <= 599) return String(value);
        if (typeof value === 'string' && /^\d{3}$/.test(value)) return value;
      }
    }
    return '';
  }
  function statusInfo(call) {
    const code = statusCodeOf(call);
    if (call && call.error) return {label: '失败', cls: 'high', code};
    return {label: '成功', cls: 'ok', code};
  }
  function duration(call) { return call && call.durationMs != null ? call.durationMs + ' ms' : '—'; }
  function valueText(value) {
    if (value == null) return '';
    return typeof value === 'string' ? value : pretty(value);
  }
  function httpInfo(call) {
    const args = (call && call.args) || {};
    const rawURL = args.url || '';
    let host = args.host || '';
    let path = '';
    let protocol = 'https:';
    if (rawURL) {
      try {
        const parsed = new URL(rawURL.includes('://') ? rawURL : 'https://' + rawURL);
        host = host || parsed.host;
        path = parsed.pathname + parsed.search;
        protocol = parsed.protocol;
      } catch (e) {
        path = rawURL;
      }
    }
    if (!host && args.header && args.header.Host) host = args.header.Host;
    const isHTTP = Boolean(host || rawURL);
    const method = (args.method || (isHTTP ? 'GET' : '')).toUpperCase();
    if (!path) path = args.path || (isHTTP ? '/' : '');
    const headers = Object.assign({}, args.header || args.headers || {});
    const body = valueText(args.data);
    const headerLines = Object.keys(headers).map(key => key + ': ' + valueText(headers[key]));
    if (host && !Object.keys(headers).some(key => key.toLowerCase() === 'host')) headerLines.unshift('Host: ' + host);
    const rawRequest = isHTTP ? `${method} ${path} HTTP/1.1\r\n${headerLines.join('\r\n')}\r\n\r\n${body}` : '';
    return {isHTTP, method, host, path, protocol, headers, body, url: host ? protocol + '//' + host + path : rawURL, rawRequest};
  }
  function rawResponse(call) {
    const code = statusCodeOf(call);
    const result = (call && (call.result || call.response)) || {};
    const headers = call && call.responseHeaders || result.header || result.headers || {};
    const bodyValue = call && call.responseBody !== undefined ? call.responseBody : (result.data !== undefined ? result.data : result);
    if (!code && !Object.keys(headers || {}).length) return valueText(bodyValue);
    const lines = Object.keys(headers || {}).map(key => key + ': ' + valueText(headers[key]));
    return `HTTP/1.1 ${code || '—'}\r\n${lines.join('\r\n')}\r\n\r\n${valueText(bodyValue)}`;
  }
  function parseRawRequest(text, originalArgs) {
    const normalized = String(text || '').replace(/\r\n/g, '\n');
    const split = normalized.indexOf('\n\n');
    const head = split >= 0 ? normalized.slice(0, split) : normalized;
    const body = split >= 0 ? normalized.slice(split + 2) : '';
    const lines = head.split('\n').filter(Boolean);
    const first = lines.shift() || '';
    const match = first.match(/^\s*([A-Za-z]+)\s+(\S+)(?:\s+HTTP\/[\d.]+)?\s*$/);
    if (!match) return null;
    const method = match[1].toUpperCase();
    const path = match[2];
    const headers = {};
    for (const line of lines) {
      const idx = line.indexOf(':');
      if (idx > 0) headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim();
    }
    const host = headers.Host || headers.host || '';
    if (!host) return null;
    const args = JSON.parse(JSON.stringify(originalArgs || {}));
    const protocol = (args.url && String(args.url).startsWith('http://')) ? 'http:' : 'https:';
    args.method = method;
    args.host = host;
    args.url = protocol + '//' + host + path;
    args.header = headers;
    args.data = body;
    return args;
  }
  function kindOf(call) {
    if (httpInfo(call).isHTTP) return 'http';
    if (call && call.category === 'storage') return 'storage';
    const api = String(call && call.api || '');
    if (/^(systemLog|report|stat|notice|getSystemInfo|getPerformance|reportPageRender)/.test(api)) return 'system';
    return 'bridge';
  }

  function sortRows(rows, key, asc) {
    const copy = rows.slice();
    copy.sort((a, b) => {
      let av = a[key], bv = b[key];
      if (key === 'status') { av = a.error ? 1 : 0; bv = b.error ? 1 : 0; }
      if (key === 'statusCode') { av = Number(statusCodeOf(a)) || 0; bv = Number(statusCodeOf(b)) || 0; }
      if (key === 'reqSize') { av = size(a.args || a.request); bv = size(b.args || b.request); }
      if (key === 'resSize') { av = size(a.result || a.response); bv = size(b.result || b.response); }
      if (av == null) av = '';
      if (bv == null) bv = '';
      return typeof av === 'number' && typeof bv === 'number'
        ? (asc ? av - bv : bv - av)
        : (asc ? String(av).localeCompare(String(bv)) : String(bv).localeCompare(String(av)));
    });
    return copy;
  }
  function sortHeader(label, key, state) {
    const active = state.key === key;
    const triangle = active ? (state.asc ? '▲' : '▼') : '▲▼';
    return `<button class="dynamic-sort" data-sort="${key}">${esc(label)}<span class="dynamic-sort-mark ${active ? 'on' : ''}">${triangle}</span></button>`;
  }
  function pager(total, page, pageSize) {
    const pages = Math.max(1, Math.ceil(total / pageSize));
    page = Math.min(Math.max(1, page), pages);
    return `<div class="dynamic-pager"><button data-page="${page - 1}" ${page <= 1 ? 'disabled' : ''}>上一页</button><span>第 ${page} / ${pages} 页 · 共 ${total} 条</span><button data-page="${page + 1}" ${page >= pages ? 'disabled' : ''}>下一页</button></div>`;
  }
  function bindPager(root, onPage) {
    root.querySelectorAll('[data-page]').forEach(btn => btn.addEventListener('click', () => { if (!btn.disabled) onPage(Number(btn.dataset.page)); }));
  }

  function showWindow(value) {
    return pretty(value);
  }

  function openDetail(options) {
    const modal = document.createElement('div');
    modal.className = 'dynamic-modal-overlay';
    modal.style.display = 'flex';
    const requestText = options.requestText || pretty(options.request || {});
    const responseHTML = options.responseText ? esc(options.responseText) : highlight(options.response);
    modal.innerHTML = `<div class="dynamic-modal dynamic-capture-modal" role="dialog" aria-modal="true"><div class="dynamic-modal-head"><h2>${esc(options.title || '捕获详情')}</h2><div class="dynamic-modal-head-actions"><span class="dynamic-badge" id="dynCaptureStatus"></span><button class="dynamic-modal-close" data-close>关闭</button></div></div><div class="dynamic-modal-body"><div class="dynamic-meta-grid" id="dynCaptureMeta">${options.meta.map(([k, v]) => `<div><span>${esc(k)}</span><strong>${esc(v)}</strong></div>`).join('')}</div><div class="dynamic-capture-split"><section><div class="dynamic-capture-title">请求包 <span id="dynReqSize" class="meta">${formatSize(size(requestText))}</span></div><textarea class="dynamic-textarea dynamic-json-editor dynamic-capture-editor" id="dynCaptureRequest" spellcheck="false">${esc(requestText)}</textarea><div class="dynamic-actions"><button data-send>发送请求</button><button data-reset>恢复原始请求</button><span class="dynamic-muted" id="dynCaptureHint">未发送</span></div></section><section><div class="dynamic-capture-title">响应包 <span id="dynResSize" class="meta">${formatSize(size(options.responseText || options.response))}</span></div><pre class="dynamic-json-view dynamic-capture-response dynamic-capture-editor" id="dynCaptureResponse">${responseHTML}</pre></section></div></div></div>`;
    document.body.appendChild(modal);
    const close = () => modal.remove();
    modal.querySelector('[data-close]').addEventListener('click', close);
    modal.addEventListener('click', event => { if (event.target === modal) close(); });
    modal.querySelector('[data-reset]').addEventListener('click', () => {
      document.getElementById('dynCaptureRequest').value = requestText;
      document.getElementById('dynReqSize').textContent = formatSize(size(requestText));
    });
    modal.querySelector('[data-send]').addEventListener('click', async event => {
      const text = document.getElementById('dynCaptureRequest').value;
      const data = options.parseRequest ? options.parseRequest(text) : c.parseObject(text, 'dynCaptureHint');
      if (!data) { c.out('dynCaptureHint', '请求包格式不正确'); return; }
      c.setBusy(event.currentTarget, true);
      try {
        const result = await options.onSend(data);
        const response = (result && result.response) || result || {};
        const sentText = options.formatRequest ? options.formatRequest((result && result.request) || data) : pretty((result && result.request) || data);
        const responseText = options.formatResponse ? options.formatResponse(response) : pretty(response);
        document.getElementById('dynCaptureResponse').textContent = responseText;
        document.getElementById('dynResSize').textContent = formatSize(size(responseText));
        document.getElementById('dynCaptureHint').textContent = '已发送 · 请求 ' + formatSize(size(sentText)) + ' · 响应 ' + formatSize(size(responseText));
        document.getElementById('dynCaptureStatus').textContent = response && response.ok === false ? '发送失败' : '已发送';
        document.getElementById('dynCaptureStatus').className = 'dynamic-badge ' + (response && response.ok === false ? 'high' : 'on');
      } finally { c.setBusy(event.currentTarget, false); }
    });
    const status = statusInfo(options.call || {});
    const badge = document.getElementById('dynCaptureStatus');
    if (badge) { badge.textContent = status.label + (status.code ? ' ' + status.code : ''); badge.className = 'dynamic-badge ' + status.cls; }
  }

  d.capture = {pretty, highlight, size, formatSize, statusCodeOf, statusInfo, duration, httpInfo, rawResponse, parseRawRequest, kindOf, sortRows, sortHeader, pager, bindPager, openDetail};
})();
