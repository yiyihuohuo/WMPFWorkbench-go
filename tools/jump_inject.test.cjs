'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');

const injectScript = `(() => {
  const target = "wxde8ac0a21135c07d";
  const handler = "goYY";
  const frame = document.querySelector('iframe[name=appContext]');
  if (!frame || !frame.contentWindow) return {ok:false, stage:'inject', error:'appContext iframe missing'};
  const w = frame.contentWindow;
  const pages = typeof w.getCurrentPages === 'function' ? w.getCurrentPages() : [];
  const page = pages[pages.length - 1];
  if (!page) return {ok:false, stage:'inject', error:'no current page'};
  if (!page.__wmpfOriginalJumpHandler) page.__wmpfOriginalJumpHandler = page[handler];
  if (typeof page[handler] !== 'function') return {ok:false, stage:'inject', error:'source handler missing', handler};
  page[handler] = function () {
    const api = w.wx || w.WeixinJSBridge;
    if (!api || typeof api.navigateToMiniProgram !== 'function') {
      w.__wmpfJumpEvidence = {target, state:'fail', errMsg:'navigateToMiniProgram unavailable'};
      return undefined;
    }
    w.__wmpfJumpEvidence = {target, state:'invoked'};
    return api.navigateToMiniProgram({
      appId: target,
      envVersion: 'release',
      success(r) { w.__wmpfJumpEvidence = {target, state:'success', errMsg:(r && r.errMsg) || 'navigateToMiniProgram:ok'}; },
      fail(r) { w.__wmpfJumpEvidence = {target, state:'fail', errMsg:r && r.errMsg || 'navigateToMiniProgram:fail'}; },
      complete(r) { w.__wmpfJumpEvidence.complete = (r && r.errMsg) || 'complete'; }
    });
  };
  return {ok:true, stage:'inject', handler, route:page.route || '', target};
})()`;

test('injected handler replaces goYY, records evidence, and preserves original', () => {
  let lastArgs = null;
  const page = {
    route: 'pages/selectMiniApp/selectMiniApp',
    goYY() { return 'original'; },
  };
  const appWindow = {
    getCurrentPages: () => [page],
    wx: {
      navigateToMiniProgram(args) {
        lastArgs = args;
        args.success({ errMsg: 'navigateToMiniProgram:ok' });
        args.complete({ errMsg: 'navigateToMiniProgram:ok' });
        return 'jumped';
      },
    },
  };
  const sandbox = {
    document: {
      querySelector(sel) {
        assert.equal(sel, 'iframe[name=appContext]');
        return { contentWindow: appWindow };
      },
    },
  };
  const result = vm.runInNewContext(injectScript, sandbox);
  assert.equal(result.ok, true);
  assert.equal(result.target, 'wxde8ac0a21135c07d');
  assert.equal(page.goYY(), 'jumped');
  assert.equal(lastArgs.appId, 'wxde8ac0a21135c07d');
  assert.equal(lastArgs.envVersion, 'release');
  assert.deepEqual({ ...appWindow.__wmpfJumpEvidence }, {
    target: 'wxde8ac0a21135c07d',
    state: 'success',
    errMsg: 'navigateToMiniProgram:ok',
    complete: 'navigateToMiniProgram:ok',
  });
  assert.equal(page.__wmpfOriginalJumpHandler(), 'original');
});
