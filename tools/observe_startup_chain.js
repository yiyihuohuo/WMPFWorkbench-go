'use strict';
// Observation only: no replacements, argument/return changes or memory writes.
// Loaded only by observe_startup_chain.py into the host it creates and its BROWSERs.
const observedModules = new Set();
function emit(event, data) {
    send(Object.assign({event: event, pid: Process.id,
        tid: Process.getCurrentThreadId(), time_ms: Date.now()}, data || {}));
}
function attempt(fn) { try { return fn(); } catch (_) { return null; } }
function trace(ctx) {
    return attempt(() => Thread.backtrace(ctx, Backtracer.ACCURATE).slice(0, 10)
        .map(a => { const m = Process.findModuleByAddress(a);
            return m ? m.name + '+' + a.sub(m.base) : a.toString(); }));
}
function runtimeInfo(p) {
    return {runtime: p.toString(),
        version: attempt(() => p.add(0x3c0).readU32()),
        preload: attempt(() => p.add(0x550).readU8()),
        appid: attempt(() => {
            const s = p.add(0x98);
            const mark = s.add(23).readS8();
            const len = mark < 0 ? s.add(8).readU64().toNumber() : mark;
            if (len < 0 || len > 80) return 'invalid';
            const value = (mark < 0 ? s.readPointer() : s).readUtf8String(len);
            return /^(wx[0-9a-f]{16}|preload-[A-Za-z0-9_-]*)$/.test(value) ? value : 'other';
        })};
}
// Scans an object for inline "wx"+16hex appid strings. Used to locate where a
// runtime config keeps its appid so a later experiment can rewrite it. Read
// only: reports offset relative to the object base.
function scanAppids(p) {
    const hits = [];
    try {
        const found = Memory.scanSync(p, 0x600, '77 78');
        for (const m of found) {
            try {
                const s = m.address.readUtf8String(18);
                if (/^wx[0-9a-f]{16}$/.test(s)) {
                    hits.push({off: m.address.sub(p).toInt32(), id: s});
                }
            } catch (e) {}
        }
    } catch (e) {}
    return hits;
}
// Experiment constant: empty disables the override. Set to a real appid to
// prove the launch config rewrite works before wiring real configuration.
const HARD_FORCED_APPID = ''; // disabled: study the pure in-app jump path first
// Launch appid must NOT be rewritten: that path is bound to the app-store f27
// envelope and fails with host.applet_event code=200. Only applets arrived at
// via in-app navigation (no f27) are rewritten.
const LAUNCH_APPID = 'wx1644df8e4fe6f725';
// Reads the opt-in appid override. Env first, then a fixed file so the value
// survives the worker -> host hand-off without relying on env propagation.
function forcedAppid() {
    let v = '';
    try { v = (Process.environment.WMPF_FORCE_APPID || '').trim(); } catch (e) {}
    if (!v) {
        try {
            const f = new File('C:\\Windows\\Temp\\wmpf-force-appid.txt', 'r');
            const buf = f.readAll(); // ArrayBuffer; toString() would yield [object ArrayBuffer]
            const bytes = new Uint8Array(buf);
            let s = '';
            for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
            f.close();
            v = s.trim();
        } catch (e) {}
    }
    if (!v) { v = HARD_FORCED_APPID; }
    return /^wx[0-9a-f]{16}$/.test(v) ? v : '';
}
function hook(m, rva, event, enter, leave, firstIndex) {
    try {
        Interceptor.attach(m.base.add(rva), {
            onEnter(args) {
                this.first = args[firstIndex || 0];
                const data = enter ? attempt(() => enter(args, this.context)) : {};
                emit(event, data);
            },
            onLeave(ret) {
                if (leave) emit(event + '.return', attempt(() => leave(this.first, ret)));
            }
        });
    } catch (error) { emit('hook_error', {event_name:event, error:String(error)}); }
}
function installHost(m) {
    const proxy = a => ({proxy:a[0].toString()});
    hook(m, 0x1bafc0, 'host.InitCore', proxy);
    hook(m, 0x1ba090, 'host.ReBind', proxy);
    hook(m, 0x1bbc50, 'host.LaunchAndBind', proxy);
    hook(m, 0x1bd350, 'host.OnChannelConnected', a => ({proxy:a[0].sub(8).toString()}));
    hook(m, 0x0d79b0, 'host.OnChannelConnected.response');
    hook(m, 0x1be860, 'host.Heartbeat', a => ({proxy:a[0].sub(8).toString()}));
    hook(m, 0x1bd950, 'host.CheckHeartbeat', a => ({proxy:a[0].toString(),
        received: a[0].add(401).readU8(), suspended:a[0].add(402).readU8()}));
    hook(m, 0x1bab80, 'host.TerminateBrowser', (a,c) => ({proxy:a[0].toString(), stack:trace(c)}));
    hook(m, 0x1bc060, 'host.Reset', (a,c) => ({proxy:a[0].toString(), stack:trace(c)}));
    hook(m, 0x1be940, 'host.ReLaunch', proxy);
    hook(m, 0x1bd1c0, 'host.EnsureConnected.timeout', proxy);
}
function installBrowser(m) {
    const ilinkInfo = p => ({instance:p.toString(),
        initialized:attempt(() => p.add(305).readU8()),
        logged_in:attempt(() => p.add(306).readU8())});
    hook(m, 0x2256160, 'ilink.Initialize', a => Object.assign(ilinkInfo(a[0]),
        {sdk_mode:a[4].toUInt32() & 255}), ilinkInfo);
    // ThirdAppLogin's secondary this points 16 bytes into the same instance.
    hook(m, 0x2260fd0, 'ilink.ThirdAppLogin', a => ilinkInfo(a[0].sub(16)),
        p => ilinkInfo(p.sub(16)));
    hook(m, 0x2261a00, 'ilink.ThirdAppLogin.result', a => Object.assign(ilinkInfo(a[0]),
        {login_type:a[4].toUInt32(), error_code:a[5].toInt32()}), ilinkInfo);
    hook(m, 0x2260ee0, 'ilink.Login.result', a => Object.assign(ilinkInfo(a[0]),
        {login_type:a[2].toUInt32(), error_code:a[3].toInt32()}), ilinkInfo);
    hook(m, 0x22622e0, 'ilink.Logout.result', a => Object.assign(ilinkInfo(a[0]),
        {error_code:a[2].toUInt32()}), ilinkInfo);
    hook(m, 0x225da70, 'ilink.LaunchApplet', a => ilinkInfo(a[0]));
    // JsApiNavigateToMiniProgram. RVA verified against the LIVE 5.10.7300.414
    // flue.dll (210MB): xref of "JsApiNavigateToMiniProgram appId =" (RVA
    // 0xb1f8006) resolves uniquely to 0x268e190..0x268f654. The old 4.2.0.13
    // IDB RVA 0x2eb7970 does NOT fire on this runtime.
    // Signature from decompile: rcx=this, rdx=JS args obj, r8=options map,
    // r9=callback. appId is pulled from args via sub_1826D7120(map,"appId").
    // Diagnostic: hook the generic API error->message mapper. case 2 (ecx==2)
    // formats "No permission."; its backtrace reveals the real caller when a
    // navigateToMiniProgram permission check fails in native.
    hook(m, 0xa071530, 'nav.errMessageMapper', (a, ctx) => ({code: a[0].toUInt32(), stack: trace(ctx)}));
    hook(m, 0x268e190, 'nav.JsApiNavigateToMiniProgram', (a, ctx) => {
        const obj = a[1];
        const sso = (p) => attempt(() => {
            const mark = p.add(23).readS8();
            const len = mark < 0 ? p.add(8).readU64().toNumber() : mark;
            if (len < 0 || len > 80) return 'invalid';
            return (mark < 0 ? p.readPointer() : p).readUtf8String(len);
        });
        return {this: a[0].toString(), args: obj ? obj.toString() : null,
            appId: obj ? sso(obj.add(40)) : null,
            toAppId: obj ? sso(obj.add(64)) : null,
            stack: trace(ctx)};
    }, (p, ret) => ({args: p ? p.toString() : null, ret: ret.toString()}), 1);
    hook(m, 0x2376870, 'browser.Initialize', a => ({runtime_impl:a[0].toString()}),
        p => ({runtime_impl:p.toString(), system_version:p.add(32).readU32()}));
    hook(m, 0x26bf0b0, 'browser.OnChannelConnected.send');
    hook(m, 0x2378650, 'browser.OnChannelConnected.ack', a => ({runtime_impl:a[0].toString(),
        timer:a[0].add(0x1b8).readPointer().toString()}));
    hook(m, 0x2377f20, 'browser.QuitMainLoop', (a,c) => ({runtime_impl:a[0].toString(),
        reason:a[1].toUInt32(), stack:trace(c)}));
    hook(m, 0x2379250, 'browser.DoHeartbeat');
    hook(m, 0x23794b0, 'browser.Heartbeat.ack');
    hook(m, 0x2379a90, 'browser.CheckHeartbeat', a => ({runtime_impl:a[0].toString(),
        received:a[0].add(352).readU8(), suspended:a[0].add(152).readU8()}));
    hook(m, 0x237a080, 'browser.Heartbeat.fallback');
    hook(m, 0x18d8850, 'browser.TerminateSelf', (a,c) => ({code:a[0].toUInt32(), stack:trace(c)}));
    // Experimental (opt-in via WMPF_FORCE_APPID): the launch config starts with
    // the mini-program appid inline at offset 0. Overwriting those 18 bytes in
    // place (same length) makes the runtime load a different mini program than
    // the one the app-store ticket was minted for. Off unless the env var is set,
    // so normal observation stays read-only.
    hook(m, 0x26ffd00, 'config.Parse', a => ({config:a[1].toString()}),
        p => ({config:p.toString(), version:p.add(0x308).readU32(), appids:scanAppids(p)}), 1);
    hook(m, 0x259f6c0, 'runtime.Construct', a => {
        // The config actually consumed by the runtime is a[3]. The appid may be
        // duplicated across several fields, so rewrite EVERY inline occurrence
        // inside the config object rather than trusting a single offset.
        const forced = forcedAppid();
        let rewritten = [];
        if (forced) {
            try {
                // Never touch the app-store (channel) appid: overwriting it
                // breaks the launch handshake (host.applet_event code=200).
                const KEEP = 'wxd44977328b36e647';
                const hits = Memory.scanSync(a[3], 0x2000, '77 78');
                for (const h of hits) {
                    const cur = h.address.readUtf8String(18);
                    if (!/^wx[0-9a-f]{16}$/.test(cur)) continue;
                    if (cur === forced || cur === KEEP || cur === LAUNCH_APPID) continue;
                    h.address.writeUtf8String(forced);
                    rewritten.push({off: h.address.sub(a[3]).toInt32(), from: cur});
                }
                if (rewritten.length) emit('config.force_appid', {to: forced, hits: rewritten});
            } catch (e) {
                emit('config.force_appid_error', {error: String(e), stage: 'construct'});
            }
        }
        // Dump http(s) strings in the config: the package URLs are what actually
        // decide which code the applet loads, so they must match the target appid.
        const urls = [];
        try {
            for (const h of Memory.scanSync(a[3], 0x2000, '68 74 74 70')) {
                try {
                    const s = h.address.readUtf8String(160);
                    urls.push({off: h.address.sub(a[3]).toInt32(), v: s.slice(0, 110)});
                } catch (e) {}
            }
        } catch (e) {}
        return {runtime:a[0].toString(), config:a[3].toString(),
            source_version:a[3].add(0x308).readU32(),
            forced: rewritten.length ? rewritten : null, urls: urls.slice(0, 8)};
    }, runtimeInfo);
    hook(m, 0x25a4ac0, 'config.Assign', a => ({config:a[0].toString(),
        source_version:a[1].add(0x308).readU32()}),
        p => ({config:p.toString(), version:p.add(0x308).readU32()}));
    hook(m, 0x25ac020, 'runtime.StartPreload', a => runtimeInfo(a[0]));
    hook(m, 0x25ac620, 'runtime.ReusePreload', a => Object.assign(runtimeInfo(a[0]),
        {source_version:a[2].add(0x308).readU32()}), runtimeInfo);
    hook(m, 0x25de1d0, 'container.OnLoadStart', a => Object.assign(
        runtimeInfo(a[0].add(64).readPointer()),
        {container:a[0].toString(), is_main_frame:a[1].toUInt32() & 255}));
    hook(m, 0x2bac420, 'devtools.Start', a => Object.assign(runtimeInfo(a[0].add(8).readPointer()),
        {server:a[0].toString(), mode:a[0].add(41).readU8()}));
    hook(m, 0x7da8170, 'devtools.SetupContext', a => Object.assign(
        runtimeInfo(a[0].add(8).readPointer()), {server:a[0].toString()}));
}
function installApi(m) {
    if (!/^(kernel32|kernelbase)\.dll$/i.test(m.name)) return;
    const terminate = m.findExportByName('TerminateProcess');
    if (terminate && !observedModules.has(terminate.toString())) {
        observedModules.add(terminate.toString());
        Interceptor.attach(terminate, {onEnter(a) {
            emit('api.TerminateProcess', {handle:a[0].toString(), code:a[1].toUInt32(), stack:trace(this.context)});
        }});
    }
    const exit = m.findExportByName('ExitProcess');
    if (exit && !observedModules.has(exit.toString())) {
        observedModules.add(exit.toString());
        Interceptor.attach(exit, {onEnter(a) {
            emit('api.ExitProcess', {code:a[0].toUInt32(), stack:trace(this.context)});
        }});
    }
}
// Diagnostic: locate native "no permission" handling for cross-mini-program jumps.
function scanNavPermission(m) {
    const terms = ['no permission', 'navigateToMiniProgram', 'checkBeforeNavigateToMiniProgram'];
    for (const term of terms) {
        const pat = [];
        for (let i = 0; i < term.length; i++) pat.push(term.charCodeAt(i).toString(16).padStart(2, '0'));
        let hits = 0;
        Memory.scan(m.base, m.size, pat.join(' '), {
            onMatch(addr) {
                hits++;
                if (hits <= 8) emit('nav_scan_hit', {term: term, off: addr.sub(m.base).toString()});
            },
            onComplete() { emit('nav_scan_done', {term: term, hits: hits}); }
        });
    }
}
Process.attachModuleObserver({onAdded(m) {
    if (m.name.toLowerCase() === 'wmpf_host_export_x64.dll') {
        installHost(m); emit('installed.host', {base:m.base.toString()});
    } else if (m.name.toLowerCase() === 'flue.dll') {
        installBrowser(m); emit('installed.browser', {base:m.base.toString()});
    }
    installApi(m);
}});
emit('observer_ready');
