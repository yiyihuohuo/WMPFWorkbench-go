(() => {
  const d = window.WMPFDynamic;
  if (!d) return;
  const c = d.common;
  const order = ['cloud', 'userscript', 'wxapi', 'route', 'scan'];
  let built = false;
  let active = 'cloud';
  const byKey = () => Object.fromEntries((d.panels || []).map(p => [p.key, p]));

  function build() {
    if (built) return;
    const root = document.getElementById('dynamicRoot');
    if (!root) return;
    const map = byKey();
    const panels = order.map(key => map[key]).filter(Boolean);
    if (!panels.length) return;
    built = true;
    root.innerHTML = `<div class="dynamic-context" id="dynContext">
      <div><span class="dynamic-context-label">当前目标</span><strong id="dynCtxAppid">未启动</strong></div>
      <div><span class="dynamic-context-label">模式</span><span id="dynCtxMode">—</span></div>
      <div><span class="dynamic-context-label">CDP</span><span id="dynCtxCdp">未连接</span></div>
      <div><span class="dynamic-context-label">包来源</span><span id="dynCtxSource">—</span></div>
      <button class="dynamic-context-refresh" id="dynCtxRefresh">刷新目标</button>
    </div>
    <div class="dynamic-shell">
      <div class="dynamic-subnav">${panels.map((p, i) => `<button data-subpanel="${p.key}" class="${i === 0 ? 'active' : ''}">${c.esc(p.title)}</button>`).join('')}</div>
      <div class="dynamic-content">${panels.map((p, i) => `<div class="dynamic-panel ${i === 0 ? 'active' : ''}" id="dynPanel-${p.key}">${p.html}</div>`).join('')}</div>
    </div>`;
    panels.forEach(panel => {
      const rootEl = document.getElementById('dynPanel-' + panel.key);
      if (panel.init) panel.init(rootEl);
    });
    root.querySelectorAll('[data-subpanel]').forEach(button => button.addEventListener('click', () => {
      active = button.dataset.subpanel;
      root.querySelectorAll('[data-subpanel]').forEach(b => b.classList.toggle('active', b === button));
      root.querySelectorAll('.dynamic-panel').forEach(panel => panel.classList.toggle('active', panel.id === 'dynPanel-' + active));
      byKey()[active]?.refresh?.();
    }));
    document.getElementById('dynCtxRefresh')?.addEventListener('click', refreshContext);
  }

  async function refreshContext() {
    const runtime = await c.invoke('runtime_status', {include_processes: false}, null);
    const session = ((runtime && runtime.sessions) || []).filter(row => row.state !== 'closed' && row.state !== 'error').slice(-1)[0];
    const appid = session && (session.active_appid || session.appid);
    document.getElementById('dynCtxAppid').textContent = appid || '未启动';
    document.getElementById('dynCtxCdp').textContent = session && session.cdp_websocket ? '已连接' : '未连接';
    document.getElementById('dynCtxMode').textContent = session ? [session.launch_mode || 'direct', session.listing_state || '', session.state || ''].filter(Boolean).join(' · ') : '—';
    const source = document.getElementById('dynCtxSource');
    if (!appid) { source.textContent = '—'; return; }
    const pkg = await c.invoke('package.resolve', {appid}, null);
    source.textContent = (pkg && pkg.package_sources && pkg.package_sources.join(', ')) || (pkg && pkg.package_source) || '未找到';
  }

  d.activate = () => {
    build();
    refreshContext();
    byKey()[active]?.refresh?.();
  };
})();
