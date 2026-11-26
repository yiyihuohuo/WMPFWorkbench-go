(() => {
  const d = window.WMPFDynamic;
  const c = d.common;
  const esc = c.esc;
  let findings = [];

  function renderFindings() {
    const box = document.getElementById('dynScanFindings');
    if (!box) return;
    if (!findings.length) { box.innerHTML = c.empty('没有发现匹配项，或还没有运行扫描。'); return; }
    box.innerHTML = `<table class="dynamic-table"><thead><tr><th>等级</th><th>规则</th><th>文件</th><th>位置</th><th>代码上下文 / 原始匹配</th></tr></thead><tbody>${findings.map(f => `<tr><td class="dynamic-severity ${esc(f.severity)}">${esc(f.severity)}</td><td>${esc(f.rule)}</td><td title="${esc(f.file)}">${esc(f.file_display || f.file)}</td><td>${f.line}:${f.column || 1}</td><td><code class="dynamic-snippet">${esc(f.snippet || f.match)}</code><div class="dynamic-actions"><button data-copy-finding="${esc(f.match)}">复制原始内容</button></div></td></tr>`).join('')}</tbody></table>`;
    box.querySelectorAll('[data-copy-finding]').forEach(btn => btn.addEventListener('click', () => c.copy(btn.dataset.copyFinding)));
  }

  function ensureModal() {
    let modal = document.getElementById('dynRulesModal');
    if (modal) return modal;
    modal = document.createElement('div');
    modal.id = 'dynRulesModal';
    modal.className = 'dynamic-modal-overlay';
    modal.innerHTML = `<div class="dynamic-modal" role="dialog" aria-modal="true" aria-label="扫描规则"><div class="dynamic-modal-head"><h2>扫描规则</h2><button class="dynamic-modal-close" data-close-rules>关闭</button></div><div class="dynamic-modal-body"><p class="muted small">修改后立即保存；正则不合法会被后端拒绝。</p><div class="dynamic-list" id="dynRuleList"></div><div class="dynamic-rule-new"><h3>新增规则</h3><label class="dynamic-label">名称</label><input class="dynamic-input" id="dynRuleName"><label class="dynamic-label">正则</label><input class="dynamic-input" id="dynRulePattern"><label class="dynamic-label">严重度</label><select class="dynamic-input" id="dynRuleSeverity"><option>info</option><option>low</option><option>medium</option><option>high</option></select><div class="dynamic-actions"><button data-act="rule-add">添加规则</button></div></div></div></div>`;
    document.body.appendChild(modal);
    modal.querySelector('[data-close-rules]').addEventListener('click', () => { modal.style.display = 'none'; });
    modal.addEventListener('click', event => { if (event.target === modal) modal.style.display = 'none'; });
    modal.querySelector('[data-act="rule-add"]').addEventListener('click', async () => {
      await c.invoke('scan.rules', {action: 'add', name: document.getElementById('dynRuleName').value, pattern: document.getElementById('dynRulePattern').value, severity: document.getElementById('dynRuleSeverity').value}, null);
      refreshRules();
    });
    return modal;
  }

  async function refreshRules() {
    const result = await c.invoke('scan.rules', {action: 'list'}, null);
    const box = document.getElementById('dynRuleList');
    if (!box) return;
    const rules = (result && result.rules) || [];
    box.innerHTML = rules.map(rule => `<div class="dynamic-rule-row" data-rule="${esc(rule.id)}"><div class="dynamic-rule-fields"><input class="dynamic-input" data-rule-name value="${esc(rule.name)}"><input class="dynamic-input" data-rule-pattern value="${esc(rule.pattern)}"><select class="dynamic-input" data-rule-severity><option ${rule.severity === 'info' ? 'selected' : ''}>info</option><option ${rule.severity === 'low' ? 'selected' : ''}>low</option><option ${rule.severity === 'medium' ? 'selected' : ''}>medium</option><option ${rule.severity === 'high' ? 'selected' : ''}>high</option></select><label class="dynamic-check"><input type="checkbox" data-rule-enabled ${rule.enabled ? 'checked' : ''}>启用</label></div><div class="dynamic-actions"><button data-rule-save>保存</button><button data-rule-remove>删除</button></div></div>`).join('') || c.empty('没有扫描规则。');
    box.querySelectorAll('[data-rule]').forEach(row => {
      const id = row.dataset.rule;
      row.querySelector('[data-rule-save]')?.addEventListener('click', async () => {
        await c.invoke('scan.rules', {action: 'update', id, name: row.querySelector('[data-rule-name]').value, pattern: row.querySelector('[data-rule-pattern]').value, severity: row.querySelector('[data-rule-severity]').value, enabled: row.querySelector('[data-rule-enabled]').checked}, null);
        refreshRules();
      });
      row.querySelector('[data-rule-remove]')?.addEventListener('click', async () => { await c.invoke('scan.rules', {action: 'remove', id}, null); refreshRules(); });
    });
  }

  async function openRules() {
    const modal = ensureModal();
    modal.style.display = 'flex';
    await refreshRules();
  }

  d.register({
    key: 'scan', title: '敏感扫描',
    html: `<div class="card"><h2>扫描当前小程序</h2><p class="muted small">默认扫描当前打开小程序已还原的源码；没有源码时会自动提交解密任务。</p><div class="dynamic-target-inline" id="dynScanTarget">当前目标：未启动</div><div class="dynamic-actions"><button data-act="scan-rules">规则列表</button><button data-act="scan-run">扫描当前小程序</button><button data-act="scan-report">打开报告</button></div><div class="dynamic-detail" id="dynScanOut">—</div></div><div class="card"><h2>扫描结果</h2><div class="dynamic-list" id="dynScanFindings"></div></div>`,
    init(root) {
      root.querySelector('[data-act="scan-rules"]').addEventListener('click', openRules);
      root.querySelector('[data-act="scan-run"]').addEventListener('click', async e => { const r = await c.invoke('scan.run', {}, 'dynScanOut', e.currentTarget); findings = (r && r.findings) || []; renderFindings(); });
      root.querySelector('[data-act="scan-report"]').addEventListener('click', async () => { if (c.api() && c.api().OpenReport) { const r = await c.api().OpenReport(); c.out('dynScanOut', r || '已请求打开报告'); } });
    },
    async refresh() {
      const runtime = await c.invoke('runtime_status', {include_processes: false}, null);
      const session = ((runtime && runtime.sessions) || []).filter(row => row.state !== 'closed' && row.state !== 'error').slice(-1)[0];
      const target = document.getElementById('dynScanTarget');
      if (target) target.textContent = session ? '当前目标：' + (session.active_appid || session.appid) + ' · ' + (session.launch_mode || 'direct') : '当前目标：未启动';
      renderFindings();
    }
  });
})();
