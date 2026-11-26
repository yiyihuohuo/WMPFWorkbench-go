'use strict';
// Minimal Frida script for owned WeChatAppEx children.
// Rewrites the destination AppID inside the native JSAPI implementation.
function emit(event, data) {
    send(Object.assign({event: event, pid: Process.id}, data || {}));
}
function attempt(fn) { try { return fn(); } catch (_) { return null; } }
function readTarget() {
    let v = '';
    try { v = (Process.environment.WMPF_JUMP_TARGET_APPID || '').trim(); } catch (_) {}
    if (!v) {
        try {
            const f = new File('C:\\Windows\\Temp\\wmpf-jump-target-appid.txt', 'r');
            const bytes = new Uint8Array(f.readAll());
            f.close();
            for (let i = 0; i < bytes.length; i++) v += String.fromCharCode(bytes[i]);
            v = v.trim();
        } catch (_) {}
    }
    return /^wx[0-9a-f]{16}$/.test(v) ? v : '';
}
function readSso(p) {
    return attempt(() => {
        const mark = p.add(23).readS8();
        const len = mark < 0 ? p.add(8).readU64().toNumber() : mark;
        if (len < 0 || len > 80) return null;
        const value = (mark < 0 ? p.readPointer() : p).readUtf8String(len);
        return /^wx[0-9a-f]{16}$/.test(value) ? value : null;
    });
}
function writeSso(p, value) {
    // Valid AppIDs are 18 chars, so the inline short-string form fits.
    p.writeUtf8String(value);
    p.add(23).writeS8(value.length);
}
function asciiPattern(text) {
    let out = '';
    for (let i = 0; i < text.length; i++) {
        if (out) out += ' ';
        out += text.charCodeAt(i).toString(16).padStart(2, '0');
    }
    return out;
}
function readScanFlag() {
    try {
        const f = new File('C:\\Windows\\Temp\\wmpf-native-xref-scan.flag', 'r');
        const bytes = new Uint8Array(f.readAll());
        f.close();
        let text = '';
        for (let i = 0; i < bytes.length; i++) text += String.fromCharCode(bytes[i]);
        return text.trim() === '1';
    } catch (_) {
        return false;
    }
}
function findAllStrings(m, text) {
    const pattern = asciiPattern(text);
    const ranges = attempt(() => m.enumerateRanges('r--')) || [];
    const found = [];
    for (const range of ranges) {
        const hits = attempt(() => Memory.scanSync(range.base, range.size, pattern)) || [];
        for (const hit of hits) found.push(hit.address);
    }
    return found;
}
function findXrefs(m, stringAddresses) {
    const patterns = [
        '48 8d 05 ?? ?? ?? ??', '48 8d 0d ?? ?? ?? ??', '48 8d 15 ?? ?? ?? ??',
        '48 8d 1d ?? ?? ?? ??', '48 8d 25 ?? ?? ?? ??', '48 8d 2d ?? ?? ?? ??',
        '48 8d 35 ?? ?? ?? ??', '48 8d 3d ?? ?? ?? ??',
        '4c 8d 05 ?? ?? ?? ??', '4c 8d 0d ?? ?? ?? ??', '4c 8d 15 ?? ?? ?? ??',
        '4c 8d 1d ?? ?? ?? ??', '4c 8d 25 ?? ?? ?? ??', '4c 8d 2d ?? ?? ?? ??',
        '4c 8d 35 ?? ?? ?? ??', '4c 8d 3d ?? ?? ?? ??'
    ];
    const wanted = {};
    stringAddresses.forEach((item) => { wanted[item.address.toString()] = item.name; });
    const ranges = attempt(() => m.enumerateRanges('r-x')) || [];
    const refs = [];
    const seen = {};
    for (const range of ranges) {
        for (const pattern of patterns) {
            const hits = attempt(() => Memory.scanSync(range.base, range.size, pattern)) || [];
            for (const hit of hits) {
                const disp = attempt(() => hit.address.add(3).readS32());
                if (disp === null) continue;
                const target = hit.address.add(7).add(disp).toString();
                const label = wanted[target];
                if (!label) continue;
                const key = hit.address.toString() + '|' + label;
                if (seen[key]) continue;
                seen[key] = true;
                refs.push({name: 'xref_' + label, address: hit.address, target: target});
            }
        }
    }
    return refs;
}
function findString(m) {
    const pattern = asciiPattern('JsApiNavigateToMiniProgram appId = ');
    const ranges = attempt(() => m.enumerateRanges('r--')) || [];
    for (const range of ranges) {
        const hits = attempt(() => Memory.scanSync(range.base, range.size, pattern)) || [];
        if (hits.length) return hits[0].address;
    }
    return null;
}
function findStringXref(m, stringAddress) {
    const pattern = '48 8d 15 ?? ?? ?? ??'; // lea rdx, [rip+disp32]
    const ranges = attempt(() => m.enumerateRanges('r-x')) || [];
    let scanned = 0;
    let hits = [];
    for (const range of ranges) {
        const found = attempt(() => Memory.scanSync(range.base, range.size, pattern)) || [];
        scanned += found.length;
        hits = hits.concat(found);
    }
    emit('native_hook.xref_scan', {base: m.base.toString(), ranges: ranges.length, hits: scanned});
    for (const hit of hits) {
        const disp = attempt(() => hit.address.add(3).readS32());
        if (disp === null) continue;
        const target = hit.address.add(7).add(disp);
        if (target.toString() === stringAddress.toString()) return hit.address;
    }
    return null;
}
function findOwner(lea) {
    // IDA verified owner begins with push r15/r14/r13/r12/rsi/rdi. Search a
    // bounded window before the xref, then fall back to the verified delta.
    const windowSize = 0x2000;
    const start = lea.sub(windowSize);
    const pattern = '41 57 41 56 41 55 41 54 56 57';
    const hits = attempt(() => Memory.scanSync(start, windowSize, pattern)) || [];
    let owner = null;
    for (const hit of hits) {
        if (hit.address.compare(lea) <= 0) owner = hit.address;
    }
    if (owner) return owner;
    return lea.sub(0x953);
}
const installed = new Set();
function install(m) {
    if (!m || m.name.toLowerCase() !== 'flue.dll') return;
    const key = m.base.toString();
    if (installed.has(key)) return;
    if (readScanFlag()) {
        const strings = [
            {name: 'jsapi_log', value: 'JsApiNavigateToMiniProgram appId = '},
            {name: 'api_name', value: 'navigateToMiniProgram'}
        ];
        const addresses = [];
        for (const item of strings) {
            for (const address of findAllStrings(m, item.value)) {
                addresses.push({name: item.name, address: address});
            }
        }
        const refs = findXrefs(m, addresses);
        emit('native_hook.xref_scan_result', {
            base: key, strings: addresses.length, xrefs: refs.length
        });
        for (const ref of refs) {
            try {
                Interceptor.attach(ref.address, {
                    onEnter() {
                        emit('native_hook.candidate_hit', {
                            name: ref.name,
                            rva: ref.address.sub(m.base).toString(),
                            target: ref.target
                        });
                    }
                });
            } catch (e) {
                emit('native_hook.error', {name: ref.name, error: String(e), base: key});
            }
        }
        installed.add(key);
        return;
    }
    const stringAddress = findString(m);
    if (!stringAddress) {
        emit('native_hook.string_not_found', {base: key});
        return;
    }
    // Avoid scanning executable ranges while the module is still being loaded:
    // that path caused script.error. Find the string dynamically, then use the
    // IDA-verified string -> owner delta for this runtime.
    const lea = stringAddress.sub(0x8b69523);
    const owner = lea.sub(0x953);
    installed.add(key);
    emit('native_hook.installed', {
        base: key,
        string_rva: stringAddress.sub(m.base).toString(),
        lea_rva: lea.sub(m.base).toString(),
        owner_rva: owner.sub(m.base).toString()
    });
    const candidates = [
        {name: 'jsapi_owner', address: owner, rewrite: true},
        {name: 'dialog_runner', address: m.base.add(0x23fcb90), rewrite: false},
        {name: 'err_mapper', address: m.base.add(0xa071530), rewrite: false},
        {name: 'jsapi_lea_site', address: lea, rewrite: false}
    ];
    for (const candidate of candidates) {
        try {
            Interceptor.attach(candidate.address, {
                onEnter(args) {
                    const rva = candidate.address.sub(m.base).toString();
                    const target = readTarget();
                    emit('native_hook.candidate_hit', {name: candidate.name, rva: rva, target: target});
                    if (!candidate.rewrite) return;
                    emit('native_hook.jsapi_called', {target: target});
                    if (!target) return;
                    const obj = args[1];
                    if (!obj || obj.isNull()) return;
                    const changed = [];
                    for (const off of [40, 64]) {
                        const cur = readSso(obj.add(off));
                        if (!cur || cur === target) continue;
                        writeSso(obj.add(off), target);
                        changed.push({off: off, from: cur, to: target});
                    }
                    if (changed.length) emit('native_hook.rewrite_appid', {changed: changed});
                }
            });
        } catch (e) {
            emit('native_hook.error', {error: String(e), name: candidate.name, base: key});
        }
    }
}
Process.attachModuleObserver({onAdded: install});
attempt(() => Process.enumerateModules().forEach(install));
emit('native_hook.ready', {});
