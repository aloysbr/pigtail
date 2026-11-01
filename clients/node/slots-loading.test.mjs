import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { after, test } from 'node:test';

const here = path.dirname(fileURLToPath(import.meta.url));
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'pigtail-browser-'));
after(() => fs.rmSync(tmp, { recursive: true, force: true }));

// multiline-comment-ok: the incident this file exists for, stated once
// A vitest setup file is NOT always loaded by Node. In browser mode
// (test.browser.enabled) vitest hands it to Vite, which transforms it and runs
// it inside the page. Two consequences, both measured on 2026-09-23 against
// a large monorepo (vitest 4.1.8, Chromium):
//
//   - `import client from './client.cjs'` — the chain slots.mjs -> client.mjs
//     -> client.cjs — dies at module load with
//     "SyntaxError: The requested module './client.cjs' does not provide an
//     export named 'default'". Vite serves the .cjs as ESM; Node's CJS interop,
//     which client.mjs relies on, does not exist there. 43 test files out of 43
//     failed, on a repo where nothing was wrong.
//   - even had it loaded, node:http does not exist in a page, so the transport
//     could not have worked anyway.
//
// A browser-mode run takes no CPU slot by design (it is arbitrated as a whole,
// see Plan.Grain), so Go stops injecting this file once a previous run recorded
// browser:true. But the FIRST run on an unknown config has no such record, and
// the arbiter must never be the reason a repo's tests do not run. Hence: the
// setup file itself must survive being loaded anywhere, which means the
// transport is imported lazily, and only under Node.

/**
 * Lays out a throwaway repo whose node_modules holds a fake vitest (the bare
 * specifier must resolve) and the real shipped client files.
 */
function stubRepo(name, { transport = 'real' } = {}) {
  const root = path.join(tmp, name);
  const cache = path.join(root, 'node_modules', '.cache', 'pigtail');
  fs.mkdirSync(cache, { recursive: true });
  fs.mkdirSync(path.join(root, 'node_modules', 'vitest'), { recursive: true });

  fs.writeFileSync(
    path.join(root, 'node_modules', 'vitest', 'package.json'),
    JSON.stringify({ name: 'vitest', version: '4.1.8', type: 'module', exports: './index.mjs' }),
  );
  fs.writeFileSync(
    path.join(root, 'node_modules', 'vitest', 'index.mjs'),
    `export const hooks = { beforeAll: [], afterAll: [] };
export const beforeAll = (fn) => hooks.beforeAll.push(fn);
export const afterAll = (fn) => hooks.afterAll.push(fn);
export const expect = { getState: () => ({ testPath: '/repo/src/a.stories.tsx' }) };
`,
  );

  for (const file of ['client.cjs', 'client.mjs', 'slots.mjs']) {
    fs.copyFileSync(path.join(here, file), path.join(cache, file));
  }
  if (transport === 'broken') {
    // Vite's transform leaves a module that resolves but exports no default.
    fs.writeFileSync(path.join(cache, 'client.mjs'), 'export const nothing = true;\n');
  }
  return cache;
}

/** Fails the test instead of hanging when an expectation never arrives. */
function withDeadline(promise, message, ms = 5_000) {
  return Promise.race([
    promise,
    new Promise((_, reject) => setTimeout(() => reject(new Error(message)), ms).unref()),
  ]);
}

/** Imports a stub repo's setup file and returns the hooks it registered. */
async function loadSetup(cache) {
  await import(pathToFileURL(path.join(cache, 'slots.mjs')).href);
  const vitest = await import(
    pathToFileURL(path.join(cache, '..', '..', 'vitest', 'index.mjs')).href
  );
  return vitest.hooks;
}

test('the setup file does not statically import the transport', () => {
  const src = fs.readFileSync(path.join(here, 'slots.mjs'), 'utf8');
  const staticImport = /^\s*import\s[^\n]*['"]\.\/client\.(mjs|cjs)['"]/m;
  assert.equal(
    staticImport.test(src),
    false,
    'a static import of the transport kills the whole module in a browser-mode run — import it lazily, inside the hook',
  );
});

test('loading it in a browser-like environment is harmless', async () => {
  const cache = stubRepo('browserish');
  globalThis.window = { document: {} };
  try {
    const hooks = await loadSetup(cache);
    assert.equal(hooks.beforeAll.length, 1, 'the hook is still registered');
    // No daemon listens here: a browser run must not reach out, nor throw.
    await hooks.beforeAll[0]();
    hooks.afterAll[0]();
  } finally {
    delete globalThis.window;
  }
});

test('a transport that cannot be imported does not fail the file', async () => {
  const cache = stubRepo('brokentransport', { transport: 'broken' });
  const hooks = await loadSetup(cache);
  await hooks.beforeAll[0]();
  hooks.afterAll[0]();
});

test('under Node it still takes and releases a slot', async () => {
  const cache = stubRepo('nodehappy');
  const sock = path.join(tmp, 'happy.sock');
  let closed;
  const closedOnce = new Promise((resolve) => {
    closed = resolve;
  });

  let served = false;
  const server = http.createServer((req, res) => {
    served = true;
    req.on('close', () => closed());
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.write(`${JSON.stringify({ data: { granted: true, waited_ms: 0, degraded: false } })}\n`);
  });
  await new Promise((resolve) => server.listen(sock, resolve));
  const previous = process.env.PIGTAIL_SOCKET;
  process.env.PIGTAIL_SOCKET = sock;

  try {
    const hooks = await loadSetup(cache);
    await hooks.beforeAll[0]();
    hooks.afterAll[0]();
    assert.equal(served, true, 'the lazy transport never reached the daemon');
    await withDeadline(closedOnce, 'release did not close the connection');
  } finally {
    if (previous === undefined) delete process.env.PIGTAIL_SOCKET;
    else process.env.PIGTAIL_SOCKET = previous;
    await new Promise((resolve) => server.close(resolve));
  }
});
