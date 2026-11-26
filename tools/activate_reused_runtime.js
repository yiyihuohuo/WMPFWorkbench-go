'use strict';
// Experiment: start the native devtools server AFTER a preload becomes real.
// No version/scene/login/main-frame edits; no process-wide once guard.
Process.attachModuleObserver({onAdded(m) {
    if (m.name.toLowerCase() !== 'flue.dll') return;
    const started = new Set();
    const start = new NativeFunction(m.base.add(0x2bac420), 'void', ['pointer', 'pointer']);
    // A fresh runtime can also be constructed without preload reuse.
    Interceptor.attach(m.base.add(0x2bac420), {onEnter(a) {
        try {
            const server = a[0];
            const info = runtimeInfo(server.add(8).readPointer());
            if (info.preload === 0 && info.version && /^wx[0-9a-f]{16}$/.test(info.appid)) {
                const oldMode = server.add(41).readU8();
                server.add(41).writeU8(1);
                emit('debug.enable_real', Object.assign(info, {server:server.toString(), old_mode:oldMode}));
            }
        } catch(e) { emit('debug.activate_error', {error:String(e)}); }
    }});
    Interceptor.attach(m.base.add(0x25ac620), {
        onEnter(a) { this.runtime = a[0]; },
        onLeave(ret) {
            try {
                const r = this.runtime;
                const info = runtimeInfo(r);
                if (!(ret.toUInt32() & 255) || info.preload !== 0 || !info.version ||
                    !/^wx[0-9a-f]{16}$/.test(info.appid) || started.has(r.toString())) return;
                const container = r.add(1512).readPointer();
                if (container.isNull() || !container.add(64).readPointer().equals(r))
                    throw new Error('Runtime/container identity mismatch');
                const tab = container.add(88).readPointer();
                const contents = tab.add(56).readPointer();
                const server = r.add(1480).readPointer();
                if (contents.isNull() || server.isNull() || !server.add(8).readPointer().equals(r))
                    throw new Error('Runtime/server identity mismatch');
                const oldMode = server.add(41).readU8();
                server.add(41).writeU8(1);
                started.add(r.toString());
                emit('debug.activate_reused', Object.assign(info, {server:server.toString(),
                    container:container.toString(), contents:contents.toString(), old_mode:oldMode}));
                start(server, contents);
                emit('debug.activate_reused.return', {runtime:r.toString(),
                    client:server.add(16).readPointer().toString(), agent:server.add(32).readPointer().toString()});
            } catch (e) { emit('debug.activate_error', {error:String(e)}); }
        }
    });
}});
