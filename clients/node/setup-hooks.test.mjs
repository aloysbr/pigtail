import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const here = path.dirname(fileURLToPath(import.meta.url));

// multiline-comment-ok: the regression this file exists for, stated once
// Vitest 4 parses a hook's first argument as a fixture destructuring pattern
// and throws FixtureParseError on a plain identifier — so a hook declared as
// `beforeAll(async (suite) => …)` fails EVERY test file of EVERY arbitrated
// run on vitest 4, while working fine on vitest 3. Measured against real
// vitest 3.2.7 and 4.1.11. The rule is narrow and easy to reintroduce by
// reflex when someone wants the suite name back, so it is asserted on the
// shipped file rather than left to a comment.
const hookCall = /\b(beforeAll|afterAll|beforeEach|afterEach)\(\s*(async\s*)?\(([^)]*)\)/g;

test('vitest setup hooks declare no positional parameter', () => {
  const src = fs.readFileSync(path.join(here, 'slots.mjs'), 'utf8');

  const offenders = [];
  for (const [, hook, , params] of src.matchAll(hookCall)) {
    const declared = params.trim();
    if (declared !== '' && !declared.startsWith('{')) {
      offenders.push(`${hook}(${declared})`);
    }
  }

  assert.deepEqual(
    offenders,
    [],
    `vitest 4 rejects these: ${offenders.join(', ')} — take the test file name from expect.getState().testPath instead`,
  );
});

test('the slot label comes from expect.getState()', () => {
  const src = fs.readFileSync(path.join(here, 'slots.mjs'), 'utf8');
  assert.match(src, /expect\.getState\(\)\.testPath/);
});
