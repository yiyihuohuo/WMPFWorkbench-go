'use strict';
(function () {
// Targeted diagnostic hook: install pure event hooks on every string xref and
// owner found by IDA in flue.dll 5.10.7300.414. No state is modified.
function emit(event, data) { send(Object.assign({event: event, pid: Process.id}, data || {})); }
function attempt(fn) { try { return fn(); } catch (_) { return null; } }
function validAppId(v) { return /^wx[0-9a-f]{16}$/.test(v || ''); }
function readSso(p) {
    return attempt(() => {
        const mark = p.add(23).readS8();
        const len = mark < 0 ? p.add(8).readU64().toNumber() : mark;
        if (len < 0 || len > 80) return null;
        const value = (mark < 0 ? p.readPointer() : p).readUtf8String(len);
        return validAppId(value) ? value : null;
    });
}
function toPtr(value) {
    return attempt(() => {
        if (!value) return null;
        if (typeof value.isNull === 'function' && typeof value.add === 'function') return value;
        const text = value.toString();
        if (!text || text === '0' || text === '0x0') return null;
        return ptr(text);
    });
}
function dumpContext(ctx) {
    const regs = ['rax','rcx','rdx','rbx','rsp','rbp','rsi','rdi','r8','r9','r10','r11','r12','r13','r14','r15'];
    const found = [];
    for (const reg of regs) {
        const p = toPtr(ctx[reg]);
        if (!p) continue;
        const s = readSso(p);
        if (s) found.push({where: reg, value: s});
        const raw = attempt(() => p.readByteArray(96));
        if (raw) {
            let text = '';
            const bytes = new Uint8Array(raw);
            for (let i = 0; i < bytes.length; i++) text += String.fromCharCode(bytes[i]);
            const m = text.match(/wx[0-9a-f]{16}/);
            if (m) found.push({where: reg + '+raw', value: m[0]});
        }
    }
    const sp = toPtr(ctx.rsp);
    if (sp) {
        for (let off = 0; off < 0x600; off += 8) {
            const p = sp.add(off);
            const s = readSso(p);
            if (s) found.push({where: 'rsp+' + off.toString(16), value: s});
            const q = attempt(() => p.readPointer());
            if (q && !q.isNull()) {
                const qs = readSso(q);
                if (qs) found.push({where: '[rsp+' + off.toString(16) + ']', value: qs});
            }
        }
    }
    const unique = {};
    const result = [];
    for (const f of found) {
        const key = f.where + '|' + f.value;
        if (unique[key]) continue;
        unique[key] = true;
        result.push(f);
    }
    return result.slice(0, 40);
}

const CANDIDATES = [
    {name: 'no_permission_dialog_runner_lea', rva: 0x23fcf21},
    {name: 'no_permission_dialog_runner_owner', rva: 0x23fcb90},
    {name: 'navigate_lea_268e14b', rva: 0x268e14b},
    {name: 'navigate_lea_296e40b', rva: 0x296e40b},
    {name: 'jsapi_log_lea', rva: 0x268eae3},
    {name: 'jsapi_owner', rva: 0x268e190},
    {name: 'navigate_lea_268f010', rva: 0x268f010},
    {name: 'navigate_lea_2705ad0', rva: 0x2705ad0},
    {name: 'err_mapper_lea', rva: 0xa0715b9},
    {name: 'err_mapper_owner', rva: 0xa071530}
];

const xrefInstalled = new Set();
function install(m) {
    if (!m || m.name.toLowerCase() !== 'flue.dll') return;
    const key = m.base.toString();
    if (xrefInstalled.has(key)) return;
    xrefInstalled.add(key);
    let attached = 0;
    const seen = new Set();
    for (const candidate of CANDIDATES) {
        if (seen.has(candidate.rva)) continue;
        seen.add(candidate.rva);
        try {
            Interceptor.attach(m.base.add(candidate.rva), {
                onEnter(args) {
                    emit('xref_hit', {name: candidate.name, rva: '0x' + candidate.rva.toString(16)});
                    if (candidate.name === 'navigate_lea_268e14b') {
                        const size = m.size && m.size.toNumber ? m.size.toNumber() : 0;
                        let addrs = null;
                        if (typeof Thread !== 'undefined' && typeof Backtracer !== 'undefined') {
                            addrs = attempt(() => Thread.backtrace(this.context, Backtracer.ACCURATE));
                            if (!addrs || !addrs.length) {
                                addrs = attempt(() => Thread.backtrace(this.context, Backtracer.FUZZY));
                            }
                        }
                        const frames = [];
                        if (addrs) {
                            for (const addr of addrs.slice(0, 24)) {
                                const rva = attempt(() => {
                                    const n = parseInt(addr.sub(m.base).toString(), 16);
                                    return n >= 0 && (!size || n < size) ? '0x' + n.toString(16) : null;
                                });
                                frames.push(rva || addr.toString());
                            }
                        }
                        emit('xref_backtrace', {frames: frames, count: frames.length});
                    }
                }
            });
            attached++;
        } catch (e) {
            emit('xref_scan_error', {name: candidate.name, error: String(e)});
        }
    }
    emit('xref_scan_result', {base: key, candidates: CANDIDATES.length, attached: attached});
}
Process.attachModuleObserver({onAdded: install});
attempt(() => Process.enumerateModules().forEach(install));
emit('xref_scan_ready', {});
})();
