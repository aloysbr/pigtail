import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';

import { acquireSlot, slotsEnabled, socketPath } from './client.mjs';

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'pigtail-slots-'));
after(() => fs.rmSync(tmp, { recursive: true, force: true }));

// Serves POST /cpu/slots on a Unix socket, envelope flushed then held open.
function startFakeDaemon(handler) {
  const sock = path.join(tmp, `d-${Math.random().toString(36).slice(2)}.sock`);
  const server = http.createServer(handler);
  return new Promise((resolve) => {
    server.listen(sock, () => resolve({ sock, server, close: () => new Promise((r) => server.close(r)) }));
  });
}

function grantHandler({ degraded = false, waitedMs = 0, onClose } = {}) {
  return (req, res) => {
    if (onClose) req.on('close', onClose);
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.write(`${JSON.stringify({ data: { granted: true, waited_ms: waitedMs, degraded }, meta: { request_id: 'req_test' } })}\n`);
    // Deliberately no res.end(): the connection is the slot.
  };
}

test('acquires a slot and reports it undegraded', async () => {
  const d = await startFakeDaemon(grantHandler({ waitedMs: 12 }));
  const slot = await acquireSlot({ group: 'ws1:vitest', label: 'a.test.ts', socket: d.sock });
  assert.equal(slot.degraded, false);
  assert.equal(typeof slot.release, 'function');
  slot.release();
  await d.close();
});

test('release closes the connection so the daemon frees the slot', async () => {
  let closed;
  const closedPromise = new Promise((resolve) => { closed = resolve; });
  const d = await startFakeDaemon(grantHandler({ onClose: () => closed() }));
  const slot = await acquireSlot({ group: 'ws1:vitest', socket: d.sock });
  slot.release();
  await closedPromise; // hangs the test until the server saw the disconnect
  await d.close();
});

// The grant is one JSON line, but nothing guarantees it arrives as one chunk:
// a TCP/Unix read boundary can land anywhere. The client buffers until it sees
// the newline precisely for this, and only this test proves it does.
test('parses a grant delivered across two chunks', async () => {
  const line = `${JSON.stringify({ data: { granted: true, waited_ms: 7, degraded: false }, meta: { request_id: 'req_split' } })}\n`;
  const cut = Math.floor(line.length / 2);
  const d = await startFakeDaemon((req, res) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.write(line.slice(0, cut)); // no newline yet: the client must keep waiting
    setImmediate(() => res.write(line.slice(cut)));
    // Deliberately no res.end(): the connection is the slot.
  });
  const slot = await acquireSlot({ group: 'ws1:vitest', socket: d.sock });
  assert.equal(slot.degraded, false);
  slot.release();
  await d.close();
});

test('propagates the daemon degraded flag', async () => {
  const d = await startFakeDaemon(grantHandler({ degraded: true }));
  const slot = await acquireSlot({ group: 'ws1:vitest', socket: d.sock });
  assert.equal(slot.degraded, true);
  slot.release();
  await d.close();
});

test('fails open when there is no daemon', async () => {
  const slot = await acquireSlot({ group: 'ws1:vitest', socket: path.join(tmp, 'absent.sock') });
  assert.equal(slot.degraded, true);
  slot.release(); // must not throw
});

test('fails open on a non-200 answer', async () => {
  const d = await startFakeDaemon((req, res) => {
    res.writeHead(503, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: { code: 'CPU_SLOTS_DISABLED', message: 'disabled' } }));
  });
  const slot = await acquireSlot({ group: 'ws1:vitest', socket: d.sock });
  assert.equal(slot.degraded, true);
  await d.close();
});

test('fails open when the daemon is too slow', async () => {
  const d = await startFakeDaemon((req, res) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    // Grants nothing, ever.
  });
  const started = Date.now();
  const slot = await acquireSlot({ group: 'ws1:vitest', socket: d.sock, timeoutMs: 50 });
  assert.equal(slot.degraded, true);
  assert.ok(Date.now() - started < 2000, 'timeout did not fire');
  await d.close();
});

test('PIGTAIL_CPU_SLOTS=0 is a kill switch', async () => {
  const previous = process.env.PIGTAIL_CPU_SLOTS;
  process.env.PIGTAIL_CPU_SLOTS = '0';
  try {
    assert.equal(slotsEnabled(), false);
    const d = await startFakeDaemon(grantHandler());
    const slot = await acquireSlot({ group: 'ws1:vitest', socket: d.sock });
    assert.equal(slot.degraded, true);
    await d.close();
  } finally {
    if (previous === undefined) delete process.env.PIGTAIL_CPU_SLOTS;
    else process.env.PIGTAIL_CPU_SLOTS = previous;
  }
});

test('a missing group fails open rather than throwing', async () => {
  const slot = await acquireSlot({ socket: path.join(tmp, 'absent.sock') });
  assert.equal(slot.degraded, true);
});

test('fails open when reading an option throws synchronously', async () => {
  const opts = { group: 'ws1:vitest' };
  Object.defineProperty(opts, 'socket', { get() { throw new Error('boom'); } });
  const slot = await acquireSlot(opts);
  assert.equal(slot.degraded, true);
  slot.release(); // must not throw
});

test('socketPath honours PIGTAIL_SOCKET then XDG_CONFIG_HOME', () => {
  const prevSock = process.env.PIGTAIL_SOCKET;
  const prevXdg = process.env.XDG_CONFIG_HOME;
  try {
    process.env.PIGTAIL_SOCKET = '/tmp/explicit.sock';
    assert.equal(socketPath(), '/tmp/explicit.sock');
    delete process.env.PIGTAIL_SOCKET;
    process.env.XDG_CONFIG_HOME = '/tmp/xdg';
    assert.equal(socketPath(), path.join('/tmp/xdg', 'pigtail', 'pigtail.sock'));
  } finally {
    if (prevSock === undefined) delete process.env.PIGTAIL_SOCKET;
    else process.env.PIGTAIL_SOCKET = prevSock;
    if (prevXdg === undefined) delete process.env.XDG_CONFIG_HOME;
    else process.env.XDG_CONFIG_HOME = prevXdg;
  }
});

// multiline-comment-ok: la raison du test est la panne qu'il empêche, pas ce qu'il appelle
// The timeout must not be a JS timer. A repo that calls jest.useFakeTimers()
// in its own setup replaces setTimeout with a clock only its tests advance, so
// a JS timer would never fire — and neither would jest's own beforeAll
// timeout, built on the same faked clock. The suite would hang for good rather
// than fail: measured 2026-09-23 on jest 29 + jsdom, killed after 120s.
// Freezing setTimeout here reproduces that hostile environment in plain node.
test('the acquire timeout survives frozen JS timers', async () => {
  const d = await startFakeDaemon((req, res) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    // Grants nothing, ever.
  });
  const realSetTimeout = globalThis.setTimeout;
  globalThis.setTimeout = () => ({ unref() {}, ref() {} });
  try {
    const started = realSetTimeout === globalThis.setTimeout ? 0 : Date.now();
    const slot = await acquireSlot({ group: 'ws1:jest', socket: d.sock, timeoutMs: 50 });
    assert.equal(slot.degraded, true, 'a frozen JS clock must not hang the acquire');
    assert.ok(Date.now() - started < 5000, 'timeout did not fire');
  } finally {
    globalThis.setTimeout = realSetTimeout;
    await d.close();
  }
});

// A granted slot is an idle connection held for the length of a test file. If
// the socket timer stayed armed it would fire on that silence and hand the
// slot back mid-run, so acquiring must disarm it.
test('a granted slot is not released by the idle socket timer', async () => {
  const closes = [];
  const d = await startFakeDaemon(grantHandler({ onClose: () => closes.push(Date.now()) }));
  const slot = await acquireSlot({ group: 'ws1:jest', socket: d.sock, timeoutMs: 60 });
  assert.equal(slot.degraded, false);
  await new Promise((r) => setTimeout(r, 250)); // well past the 60ms timeout
  assert.equal(closes.length, 0, 'the slot was released while still held');
  slot.release();
  await d.close();
});
