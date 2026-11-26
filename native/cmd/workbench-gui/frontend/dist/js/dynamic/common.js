(() => {
  const dynamic = window.WMPFDynamic || (window.WMPFDynamic = {panels: []});
  const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const api = () => window.__wmpfApi;
  const text = (value) => typeof value === 'string' ? value : JSON.stringify(value, null, 2);
  function out(id, value) {
    const el = document.getElementById(id);
    if (el) el.textContent = text(value);
  }
  function setBusy(button, busy, label) {
    if (!button) return;
    if (busy) {
      button.dataset.original = button.textContent;
      button.textContent = label || '处理中…';
      button.disabled = true;
    } else {
      button.textContent = button.dataset.original || button.textContent;
      button.disabled = false;
    }
  }
  async function invoke(tool, args, outputId, button) {
    setBusy(button, true);
    try {
      if (!api() || typeof api().Invoke !== 'function') throw new Error('GUI 后端未就绪');
      const result = await api().Invoke(tool, JSON.stringify(args || {}));
      if (outputId) out(outputId, result && result.ok === false ? (result.message || result.reason || result) : result);
      return result;
    } catch (e) {
      if (outputId) out(outputId, '错误：' + (e && e.message ? e.message : String(e)));
      return null;
    } finally {
      setBusy(button, false);
    }
  }
  function parseObject(value, outputId) {
    try { return value && value.trim() ? JSON.parse(value) : {}; }
    catch (e) { if (outputId) out(outputId, 'JSON 解析失败：' + e.message); return null; }
  }
  async function copy(textValue) {
    try { await navigator.clipboard.writeText(String(textValue)); } catch (e) {}
  }
  function empty(message) { return `<div class="dynamic-empty">${esc(message)}</div>`; }
  dynamic.register = (panel) => {
    if (!panel || !panel.key) return;
    dynamic.panels = dynamic.panels.filter(item => item.key !== panel.key);
    dynamic.panels.push(panel);
  };
  dynamic.common = {esc, api, text, out, setBusy, invoke, parseObject, copy, empty};
})();
