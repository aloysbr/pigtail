// pigtail vitest setup file: one CPU slot per test file, via beforeAll/afterAll.

import { afterAll, beforeAll, expect } from 'vitest';

const group = process.env.PIGTAIL_CPU_SLOT_GROUP || 'vitest';

let slot = null;

// multiline-comment-ok: a measured vitest 4 constraint that must not be lost
// The callback below declares NO parameter, and that is load-bearing. Vitest 4
// parses a hook's first argument as a fixture destructuring pattern and throws
// FixtureParseError on a plain identifier. The previous version took `(suite)`
// to label the slot with the test file: on vitest 3 that worked, on vitest 4 it
// failed *every test file of every arbitrated run* — measured on 4.1.11, and
// a large monorepo is on 4.1.8. expect.getState().testPath gives the same
// label on both majors and needs no parameter. The timeout must exceed
// ACQUIRE_TIMEOUT_MS so vitest's default 10s hookTimeout never fires first, and
// the try/catch is the structural half of fail-open: acquireSlot is written
// never to reject, but a synchronous throw inside its executor would reject the
// promise, fail the whole file, and make the arbiter the reason tests do not run.

// multiline-comment-ok: why the transport is not imported at the top of this file
// This file is not always loaded by Node. In browser mode vitest hands it to
// Vite, which transforms it and runs it inside the page — where node:http does
// not exist, and where `import … from './client.cjs'` dies at module load
// ("does not provide an export named 'default'": Vite serves the .cjs as ESM,
// without Node's CJS interop). A static import would therefore fail the whole
// file before any try/catch could run: measured 2026-09-23 on vitest 4.1.8,
// 43 test files out of 43 failed on a repo where nothing was wrong.
// A browser run takes no slot anyway (it is arbitrated whole, see Plan.Grain),
// and Go stops injecting this file once a run has recorded browser:true — but
// the first run on an unknown config has no such record. So: load the transport
// lazily, under Node only, and treat every failure as "run unarbitrated".
const underNode =
  typeof process !== 'undefined' &&
  Boolean(process.versions && process.versions.node) &&
  typeof window === 'undefined';

/** Mirrors ACQUIRE_TIMEOUT_MS in client.cjs, loaded too late to read here; a Go test catches any drift. */
const ACQUIRE_TIMEOUT_MS = 30_000;

/** Loads the transport, or null if it cannot be loaded here. */
async function transport() {
  if (!underNode) return null;
  try {
    // The URL keeps Vite's static analysis out of it; Node resolves it as usual.
    return await import(/* @vite-ignore */ new URL('./client.mjs', import.meta.url).href);
  } catch {
    return null;
  }
}

beforeAll(async () => {
  let label = '';
  try {
    label = (expect.getState().testPath || '').split('/').pop() || '';
  } catch {
    label = '';
  }
  try {
    const client = await transport();
    slot = client ? await client.acquireSlot({ group, label }) : null;
  } catch {
    slot = null;
  }
}, ACQUIRE_TIMEOUT_MS + 5_000);

afterAll(() => {
  if (!slot) return;
  const held = slot;
  slot = null;
  try {
    held.release();
  } catch {
    // release() must not fail the file either: the connection was the lease.
  }
});
