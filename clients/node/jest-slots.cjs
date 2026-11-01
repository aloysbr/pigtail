// multiline-comment-ok: pourquoi setupFilesAfterEnv et non setupFiles
// pigtail jest setup file: one CPU slot per test file, via beforeAll/afterAll.
// It has to be setupFilesAfterEnv, not setupFiles: the latter runs before the
// test framework is installed, so beforeAll does not exist there yet. Jest
// resets the module registry per test file and a worker runs one file at a
// time, so "one slot per file" maps onto jest exactly as it does onto vitest.

const { ACQUIRE_TIMEOUT_MS, acquireSlot } = require('./client.cjs');

const group = process.env.PIGTAIL_CPU_SLOT_GROUP || 'jest';

let slot = null;

/** The test file under way, for `pigtail slots` — best effort, never fatal. */
function label() {
  try {
    const state = expect.getState();
    const file = state && state.testPath;
    return file ? file.split('/').pop() : '';
  } catch {
    return '';
  }
}

// multiline-comment-ok: le try/catch est la moitié structurelle du fail-open
// The try/catch is the structural half of fail-open. acquireSlot is written
// never to reject, but its executor calls http.request() unguarded: a
// synchronous throw in there rejects the promise, the await propagates it, and
// a rejecting beforeAll fails the whole test file — the arbiter becoming the
// reason tests do not run, the one thing it must never be. The guarantee
// belongs here, not in client.cjs staying perfect forever.
beforeAll(async () => {
  try {
    slot = await acquireSlot({ group, label: label() });
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
