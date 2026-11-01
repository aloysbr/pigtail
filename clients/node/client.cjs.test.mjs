// The CJS façade is what jest loads. It must expose the same API as the ESM
// one and work against a real daemon, or jest runs silently lose arbitration.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';

import * as esm from './client.mjs';

const require = createRequire(import.meta.url);
const cjs = require('./client.cjs');

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'pigtail-cjs-'));
after(() => fs.rmSync(tmp, { recursive: true, force: true }));

test('the CJS module exposes the whole API', () => {
  for (const key of ['ACQUIRE_TIMEOUT_MS', 'socketPath', 'slotsEnabled', 'acquireSlot']) {
    assert.ok(cjs[key] !== undefined, `client.cjs is missing ${key}`);
  }
});

// The façade must re-export, not re-implement: same function objects, so a fix
// in the transport can never reach one runner and miss the other.
test('the ESM façade re-exports the very same functions', () => {
  assert.equal(esm.acquireSlot, cjs.acquireSlot);
  assert.equal(esm.socketPath, cjs.socketPath);
  assert.equal(esm.slotsEnabled, cjs.slotsEnabled);
  assert.equal(esm.ACQUIRE_TIMEOUT_MS, cjs.ACQUIRE_TIMEOUT_MS);
});

test('acquires and releases through the CJS entry point', { timeout: 10_000 }, async () => {
  const sock = path.join(tmp, 'd.sock');
  const closed = [];
  let onClose;
  const closedEvent = new Promise((r) => { onClose = r; });
  const server = http.createServer((req, res) => {
    req.on('close', () => { closed.push(1); onClose(); });
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.write(`${JSON.stringify({ data: { granted: true, degraded: false } })}\n`);
  });
  await new Promise((r) => server.listen(sock, r));
  const slot = await cjs.acquireSlot({ group: 'ws1:jest', label: 'a.test.js', socket: sock });
  assert.equal(slot.degraded, false);
  slot.release();
  // release() is req.destroy() and returns nothing: wait for the server's close.
  await closedEvent;
  assert.equal(closed.length, 1, 'release did not close the connection');
  await new Promise((r) => server.close(r));
});
