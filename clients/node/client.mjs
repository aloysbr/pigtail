// multiline-comment-ok: pourquoi une façade et non une seconde implémentation
// ESM façade over client.cjs, which holds the one implementation.
// Vitest setup files are ESM; jest's must be CJS (it lands under node_modules,
// which transformIgnorePatterns excludes from every transform). Re-exporting
// costs five lines and keeps a single transport; composing two façades from a
// shared body was the alternative, and it buys nothing here. Verified
// 2026-09-23 on vitest 3.2.7 and 4.1.11: both load this chain through
// vite-node without interop trouble.

import client from './client.cjs';

export const ACQUIRE_TIMEOUT_MS = client.ACQUIRE_TIMEOUT_MS;
export const socketPath = client.socketPath;
export const slotsEnabled = client.slotsEnabled;
export const acquireSlot = client.acquireSlot;
