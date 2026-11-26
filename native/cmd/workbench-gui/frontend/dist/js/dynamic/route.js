(() => {
  const d = window.WMPFDynamic;
  const c = d.common;
  const esc = c.esc;
  let routes = [];
  let guard = false;

  function render() {
    const box = document.getElementById('dynRouteList');
    const options = document.getElementById('dynRouteOptions');
    if (!box) return;
    if (options) options.innerHTML = routes.map(r => `<option value="${esc(r.path)}">${esc(r.category)}</option>`).join('');
    const keyword = (document.getElementById('dynRouteFilter')?.value || '').toLowerCase();
    const rows = routes.filter(r => !keyword || String(r.path).toLowerCase().includes(keyword) || String(r.category).toLowerCase().includes(keyword));
    if (!rows.length) { box.innerHTML = c.empty('没有匹配的路由。'); return; }
    const groups = {};
    rows.forEach(r => {
      const text = String(r.path);
      const group = text.includes('/') ? text.split('/').filter(Boolean).slice(0, -1).join('/') || '主包' : '主包';
      (groups[group] ||= []).push(r);
    });
    box.innerHTML = Object.keys(groups).sort().map(group => `<div class="dynamic-route-group"><div class="dynamic-group-title">${esc(group)} <span class="meta">${groups[group].length}</span></div>${groups[group].map(r => `<div class="dynamic-item dynamic-route-row"><div class="dynamic-route-main"><div class="dynamic-item-head"><span>${esc(r.path)}</span><span class="dynamic-badge">${esc(r.category)}</span></div><div class="meta">点击右侧按钮按自动回退链跳转</div></div><div class="dynamic-route-actions"><button data-route-go="${esc(r.path)}">跳转</button><button data-route-copy="${esc(r.path)}">复制</button></div></div>`).join('')}</div>`).join('');
    box.querySelectorAll('[data-route-go]').forEach(btn => btn.addEventListener('click', async e => {
      const button = e.currentTarget;
      c.out('dynRouteFeedback', '正在跳转：' + button.dataset.routeGo);
      await c.invoke('route.navigate', {path: button.dataset.routeGo, method: ''}, 'dynRouteFeedback', button);
    }));
    box.querySelectorAll('[data-route-copy]').forEach(btn => btn.addEventListener('click', () => c.copy(btn.dataset.routeCopy)));
  }

  async function refresh() {
    const result = await c.invoke('route.list', {}, null);
    routes = (result && result.routes) || [];
    render();
  }

  function updateGuard() {
    const badge = document.getElementById('dynGuardBadge');
    if (badge) { badge.textContent = guard ? '开启' : '关闭'; badge.classList.toggle('on', guard); }
  }

  d.register({
    key: 'route', title: '路由地图',
    html: `<div class="card"><h2>手动导航</h2><div class="dynamic-manual-route"><label class="dynamic-label">路由路径</label><input class="dynamic-input" id="dynRoutePath" list="dynRouteOptions" placeholder="/pages/index/index"><datalist id="dynRouteOptions"></datalist><label class="dynamic-label">导航方法</label><select class="dynamic-input" id="dynRouteMethod"><option value="navigateTo">navigateTo</option><option value="redirectTo">redirectTo</option><option value="switchTab">switchTab</option><option value="reLaunch">reLaunch</option></select><div class="dynamic-toggle-row"><span>跳转守卫</span><span class="dynamic-badge" id="dynGuardBadge">关闭</span><button data-act="route-guard-on">开启</button><button data-act="route-guard-off">关闭</button></div><div class="dynamic-actions"><button data-act="route-navigate">执行手动跳转</button><button data-act="route-list">刷新路由</button></div><div class="dynamic-detail" id="dynRouteFeedback">—</div></div></div><div class="card"><h2>路由清单</h2><div class="dynamic-actions"><input class="dynamic-input dynamic-inline" id="dynRouteFilter" placeholder="搜索路径或分类"></div><div class="dynamic-list" id="dynRouteList"></div></div>`,
    init(root) {
      document.getElementById('dynRouteFilter')?.addEventListener('input', render);
      root.querySelector('[data-act="route-list"]').addEventListener('click', refresh);
      root.querySelector('[data-act="route-guard-on"]').addEventListener('click', async e => { await c.invoke('route.guard', {enabled: true}, 'dynRouteFeedback', e.currentTarget); guard = true; updateGuard(); });
      root.querySelector('[data-act="route-guard-off"]').addEventListener('click', async e => { await c.invoke('route.guard', {enabled: false}, 'dynRouteFeedback', e.currentTarget); guard = false; updateGuard(); });
      root.querySelector('[data-act="route-navigate"]').addEventListener('click', async e => {
        const path = document.getElementById('dynRoutePath').value;
        const method = document.getElementById('dynRouteMethod').value;
        if (!path.trim()) { c.out('dynRouteFeedback', '请先选择或输入路由路径'); return; }
        await c.invoke('route.navigate', {path, method}, 'dynRouteFeedback', e.currentTarget);
      });
      updateGuard();
    },
    refresh
  });
})();
