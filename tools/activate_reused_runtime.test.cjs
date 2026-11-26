'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');
const path = require('node:path');

const scriptPath = path.join(__dirname, 'activate_reused_runtime.js');
const script = fs.readFileSync(scriptPath, 'utf8');

function runActivation(info) {
    const memory = new Map();
    const callbacks = new Map();
    const runtime = { id: 'runtime' };
    const server = pointer('server');
    memory.set('server+8', runtime);
    memory.set('server+41', 0);

    function pointer(id, offset = 0) {
        return {
            id,
            offset,
            add(n) { return pointer(id, offset + n); },
            readPointer() { return memory.get(`${id}+${offset}`) || null; },
            readU8() { return memory.get(`${id}+${offset}`) || 0; },
            writeU8(value) { memory.set(`${id}+${offset}`, value); },
            isNull() { return false; },
            equals(other) { return other?.id === id && other?.offset === offset; },
            toString() { return `${id}+${offset}`; },
        };
    }

    const module = { name: 'flue.dll', base: pointer('base') };
    const context = {
        launchConfig: { appid: 'wx1644df8e4fe6f725' },
        runtimeInfo: () => info,
        Process: { attachModuleObserver: observer => observer.onAdded(module) },
        NativeFunction: function() { return () => {}; },
        Interceptor: {
            attach(address, value) { callbacks.set(address.offset, value); },
        },
        emit() {},
        Set,
        Object,
        Error,
    };

    vm.runInNewContext(script, context, { filename: scriptPath });
    callbacks.get(0x2bac420).onEnter([server]);
    return server.add(41).readU8();
}

function runReusedRuntimeActivation(info) {
    const memory = new Map();
    const callbacks = new Map();
    const events = [];
    const starts = [];
    function pointer(id, offset = 0) {
        return {
            id,
            offset,
            add(n) { return pointer(id, offset + n); },
            readPointer() { return memory.get(`${id}+${offset}`) || null; },
            readU8() { return memory.get(`${id}+${offset}`) || 0; },
            writeU8(value) { memory.set(`${id}+${offset}`, value); },
            isNull() { return false; },
            equals(other) { return other?.id === id && other?.offset === offset; },
            toString() { return `${id}+${offset}`; },
        };
    }

    const runtime = pointer('runtime');
    const server = pointer('server');
    const container = pointer('container');
    const tab = pointer('tab');
    const contents = pointer('contents');
    memory.set('runtime+1480', server);
    memory.set('runtime+1512', container);
    memory.set('server+8', runtime);
    memory.set('server+41', 0);
    memory.set('container+64', runtime);
    memory.set('container+88', tab);
    memory.set('tab+56', contents);

    const module = { name: 'flue.dll', base: pointer('base') };
    const context = {
        launchConfig: { appid: 'wx1644df8e4fe6f725' },
        runtimeInfo: () => info,
        Process: { attachModuleObserver: observer => observer.onAdded(module) },
        NativeFunction: function() { return (...args) => starts.push(args); },
        Interceptor: {
            attach(address, value) { callbacks.set(address.offset, value); },
        },
        emit(name, payload) { events.push({ name, payload }); },
        Set,
        Object,
        Error,
    };

    vm.runInNewContext(script, context, { filename: scriptPath });
    const construct = callbacks.get(0x25ac620);
    construct.onEnter([runtime]);
    construct.onLeave({ toUInt32: () => 1 });
    construct.onEnter([runtime]);
    construct.onLeave({ toUInt32: () => 1 });
    return { mode: server.add(41).readU8(), starts, events };
}

test('enables devtools for a real mini-program reached by in-app navigation', () => {
    assert.equal(runActivation({
        runtime: 'runtime',
        appid: 'wxacdc0917b2fdfe6e',
        version: 331,
        preload: 0,
    }), 1);
});

test('keeps devtools disabled for preload and incomplete runtimes', () => {
    for (const info of [
        { appid: 'wx1644df8e4fe6f725', version: 413, preload: 1 },
        { appid: 'wx1644df8e4fe6f725', version: 0, preload: 0 },
        { appid: 'other', version: 413, preload: 0 },
    ]) {
        assert.equal(runActivation(info), 0);
    }
});

test('starts devtools once for a real post-navigation runtime', () => {
    const result = runReusedRuntimeActivation({
        runtime: 'reused-runtime',
        appid: 'wxacdc0917b2fdfe6e',
        version: 331,
        preload: 0,
    });
    assert.equal(result.mode, 1);
    assert.equal(result.starts.length, 1);
    assert.equal(result.starts[0][0].id, 'server');
    assert.equal(result.starts[0][1].id, 'contents');
    assert.equal(result.events[0].name, 'debug.activate_reused');
    assert.equal(result.events[0].payload.appid, 'wxacdc0917b2fdfe6e');
});
