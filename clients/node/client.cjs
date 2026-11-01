// multiline-comment-ok: pourquoi CJS est la forme source, et non un choix de style
// pigtail's CPU slot client: zero npm deps, node:http only, never blocks a test.
// CommonJS is the source form on purpose, and client.mjs is a thin re-export of
// it. Jest's setup file cannot be anything else: it is installed under
// node_modules/.cache/pigtail, and a repo's transformIgnorePatterns excludes
// node_modules from every transform, so nothing turns ESM into something jest
// can require. Writing the transport once as CJS and re-exporting it as ESM
// keeps one implementation for both runners; verified 2026-09-23 that vitest 3
// and vitest 4 both load the slots.mjs -> client.mjs -> client.cjs chain.

const http = require('node:http');
const os = require('node:os');
const path = require('node:path');

/** How long to wait for a grant before running unarbitrated. */
const ACQUIRE_TIMEOUT_MS = 30_000;

/** Resolve the daemon socket the same way the Go client does. */
function socketPath() {
  if (process.env.PIGTAIL_SOCKET) return process.env.PIGTAIL_SOCKET;
  const configHome = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config');
  return path.join(configHome, 'pigtail', 'pigtail.sock');
}

/** The client-side kill switch: PIGTAIL_CPU_SLOTS=0 turns arbitration off. */
function slotsEnabled() {
  return process.env.PIGTAIL_CPU_SLOTS !== '0';
}

/**
 * Acquire one CPU slot. Resolves once the daemon grants it, or immediately
 * with `degraded: true` when there is nothing to arbitrate with.
 *
 * The returned `release()` destroys the request socket. That closure is what
 * the daemon reads as "the slot is free" — there is no release endpoint.
 *
 * @param {{group?: string, label?: string, timeoutMs?: number, socket?: string}} opts
 * @returns {Promise<{release: () => void, degraded: boolean, waitedMs: number}>}
 */
function acquireSlot(opts = {}) {
  const started = Date.now();
  const failOpen = () => ({ release() {}, degraded: true, waitedMs: elapsed(started) });

  // socketPath() can throw (os.homedir() with no passwd entry); stay guarded.
  let group, label, timeoutMs, socket, enabled;
  try {
    group = opts.group;
    label = opts.label || '';
    timeoutMs = opts.timeoutMs || ACQUIRE_TIMEOUT_MS;
    socket = opts.socket || socketPath();
    enabled = slotsEnabled();
  } catch {
    return Promise.resolve(failOpen());
  }

  if (!group || !enabled) return Promise.resolve(failOpen());

  return new Promise((resolve) => {
    let settled = false;
    const done = (value) => {
      if (settled) return;
      settled = true;
      // multiline-comment-ok: timer d'inactivité et non délai d'attente — le désarmer est obligatoire
      // Disarm the socket timer before resolving: it is an *idle* timer, and a
      // granted slot is precisely an idle connection held open for minutes, so
      // leaving it armed would destroy the request mid-run and hand the slot
      // back while the tests it covers are still running.
      try { req.setTimeout(0); } catch { /* the socket is already gone */ }
      resolve(value);
    };

    const req = http.request(
      {
        socketPath: socket,
        method: 'POST',
        path: '/cpu/slots',
        headers: {
          'Content-Type': 'application/json',
          'X-Pigtail-API': 'v1',
        },
      },
      (res) => {
        if (res.statusCode !== 200) {
          res.resume();
          req.destroy();
          done(failOpen());
          return;
        }
        // One line: the daemon flushes the grant then holds the connection.
        let buffer = '';
        res.setEncoding('utf8');
        res.on('data', (chunk) => {
          if (settled) return;
          buffer += chunk;
          const newline = buffer.indexOf('\n');
          if (newline < 0) return;
          let data;
          try {
            data = JSON.parse(buffer.slice(0, newline)).data;
          } catch {
            req.destroy();
            done(failOpen());
            return;
          }
          done({
            release() { req.destroy(); },
            // data.granted is unread: the daemon always grants true today.
            degraded: Boolean(data && data.degraded),
            waitedMs: elapsed(started),
          });
        });
        res.on('error', () => { req.destroy(); done(failOpen()); });
        // The daemon closing without granting is the fail-open case too.
        res.on('end', () => done(failOpen()));
      },
    );

    req.on('error', () => done(failOpen()));

    // multiline-comment-ok: req.setTimeout plutôt que setTimeout — raison mesurée, à ne pas perdre
    // Abandon the wait as well, or a late grant stays booked to nobody forever.
    // req.setTimeout, not setTimeout: this runs inside the repo's own test
    // process, and a repo that calls jest.useFakeTimers() in its setup replaces
    // every JS timer with one that only advances when a test says so. A JS
    // timer would then never fire — and neither would jest's own beforeAll
    // timeout, built on those same faked timers, so the suite would hang
    // instead of failing. Measured 2026-09-23 on jest 29 + jsdom: >120s, killed.
    // req.setTimeout arms a libuv socket timer, out of reach of any JS clock.
    req.setTimeout(timeoutMs, () => {
      req.destroy();
      done(failOpen());
    });

    req.end(JSON.stringify({ group, label, weight: 1 }));
  });
}

/** elapsed is telemetry only: faked timers freeze Date.now(), so this can read 0. */
function elapsed(started) {
  const ms = Date.now() - started;
  return ms >= 0 ? ms : 0;
}

module.exports = { ACQUIRE_TIMEOUT_MS, socketPath, slotsEnabled, acquireSlot };
