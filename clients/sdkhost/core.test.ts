import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, writeFileSync, readdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Journal, Pending, Inbox, toPermissionResult, parseCommand, type HostFile, type Request } from './core.ts'

const tmp = () => mkdtempSync(join(tmpdir(), 'sdkhost-'))
const fixture = (n: string) => readFileSync(new URL(`./testdata/${n}`, import.meta.url), 'utf8')
const shape = (v: unknown): unknown =>
  Array.isArray(v) ? (v.length ? [shape(v[0])] : []) : v && typeof v === 'object' ? Object.fromEntries(Object.entries(v).sort().map(([k, x]) => [k, shape(x)])) : typeof v

test('seq strictly increases and resumes after rereading events.jsonl', () => {
  const dir = tmp()
  const j = new Journal(dir)
  const a = j.append('init', {}); const b = j.append('state', {})
  assert.equal(a.seq, 1); assert.equal(b.seq, 2)
  const j2 = new Journal(dir)
  assert.equal(j2.append('state', {}).seq, 3)
  const lines = readFileSync(join(dir, 'events.jsonl'), 'utf8').trim().split('\n').map((l) => JSON.parse(l))
  assert.deepEqual(lines.map((l) => l.seq), [1, 2, 3])
})

test('host.json is written atomically, never half, no tmp left', () => {
  const dir = tmp()
  const j = new Journal(dir)
  const h: HostFile = JSON.parse(fixture('host.json'))
  j.writeHost(h); j.writeHost({ ...h, turn: 2 })
  assert.equal(JSON.parse(readFileSync(join(dir, 'host.json'), 'utf8')).turn, 2)
  assert.deepEqual(readdirSync(dir).filter((f) => f.includes('tmp')), [])
})

test('contract: writeHost keys and types equal testdata/host.json; fixtures decode', () => {
  const dir = tmp()
  const h: HostFile = JSON.parse(fixture('host.json'))
  new Journal(dir).writeHost(h)
  assert.deepEqual(shape(JSON.parse(readFileSync(join(dir, 'host.json'), 'utf8'))), shape(JSON.parse(fixture('host.json'))))
  assert.equal(h.version, 1); assert.equal(h.state, 'waiting'); assert.equal(h.requests.length, 1)
  const meta = JSON.parse(fixture('meta.json'))
  for (const k of ['version', 'id', 'subject_id', 'cwd', 'config_dir', 'claude', 'mode', 'prompt', 'session_uuid', 'host_pid', 'created_at']) assert.ok(k in meta, k)
  const ev = fixture('events.jsonl').trim().split('\n').map((l) => JSON.parse(l))
  assert.deepEqual(ev.map((e) => e.seq), [1, 2, 3, 4, 5])
  const dir2 = tmp(); writeFileSync(join(dir2, 'events.jsonl'), fixture('events.jsonl'))
  assert.equal(new Journal(dir2).append('state', {}).seq, 6)
})

test('Pending.answer on unknown or already answered rid is false', async () => {
  const p = new Pending()
  assert.equal(p.answer({ rid: 'nope', behavior: 'allow' }), false)
  const { rid, done } = p.open({ kind: 'permission', tool: 'Bash', input: {} })
  assert.equal(p.list().length, 1)
  assert.equal(p.answer({ rid, behavior: 'allow' }), true)
  assert.equal((await done).rid, rid)
  assert.equal(p.answer({ rid, behavior: 'allow' }), false)
  assert.equal(p.list().length, 0)
})

const q = (multi: boolean): Request => ({ rid: 'r1', kind: 'question', tool: 'AskUserQuestion', opened_at: 'x', input: { questions: [{ question: 'Which fruits?', multiSelect: multi }] } })

test('toPermissionResult: question merges answers into updatedInput', () => {
  const r = toPermissionResult(q(false), { rid: 'r1', behavior: 'allow', answers: { 'Which fruits?': 'Apple' } })
  assert.deepEqual(r, { behavior: 'allow', updatedInput: { questions: (q(false).input as any).questions, answers: { 'Which fruits?': 'Apple' } } })
})

test('toPermissionResult: multiSelect labels joined by ", " (H10)', () => {
  const c = parseCommand(JSON.stringify({ op: 'answer', rid: 'r1', behavior: 'allow', answers: { 'Which fruits?': ['Banana', 'Date'] } }))
  assert.ok('op' in c && c.op === 'answer')
  const r = toPermissionResult(q(true), (c as any).answer) as any
  assert.equal(r.updatedInput.answers['Which fruits?'], 'Banana, Date')
})

test('toPermissionResult: allow without answers keeps input; deny carries message or default', () => {
  const req: Request = { rid: 'r', kind: 'permission', tool: 'Bash', input: { command: 'ls' }, opened_at: 'x' }
  assert.deepEqual(toPermissionResult(req, { rid: 'r', behavior: 'allow' }), { behavior: 'allow', updatedInput: { command: 'ls' } })
  assert.deepEqual(toPermissionResult(req, { rid: 'r', behavior: 'deny', message: 'non' }), { behavior: 'deny', message: 'non' })
  assert.deepEqual(toPermissionResult(req, { rid: 'r', behavior: 'deny', message: '' }), { behavior: 'deny', message: 'refusé' })
  assert.deepEqual(toPermissionResult(req, { rid: 'r', behavior: 'deny' }), { behavior: 'deny', message: 'refusé' })
})

test('parseCommand: valid ops; invalid line or op gives error, never throws', () => {
  assert.deepEqual(parseCommand('{"op":"say","text":"hi"}'), { op: 'say', text: 'hi' })
  assert.deepEqual(parseCommand('{"op":"status"}'), { op: 'status' })
  assert.deepEqual(parseCommand('{"op":"end"}'), { op: 'end' })
  assert.deepEqual(parseCommand('{"op":"answer","rid":"r1","behavior":"deny","message":"m"}'), { op: 'answer', answer: { rid: 'r1', behavior: 'deny', message: 'm' } })
  for (const bad of ['not json', '{}', '{"op":"zap"}', '{"op":"say"}', '{"op":"answer","rid":"r","behavior":"maybe"}', 'null', '[1]', '']) {
    const r = parseCommand(bad)
    assert.ok('error' in r, bad)
  }
})

test('Inbox: push before or after waiting is served in order; close ends iteration', async () => {
  const ib = new Inbox()
  ib.push('a'); ib.push('b')
  const it = ib[Symbol.asyncIterator]()
  assert.deepEqual(await it.next(), { value: 'a', done: false })
  assert.deepEqual(await it.next(), { value: 'b', done: false })
  const waiting = it.next(); ib.push('c')
  assert.deepEqual(await waiting, { value: 'c', done: false })
  const w2 = it.next(); ib.close()
  assert.equal((await w2).done, true)
  assert.equal((await it.next()).done, true)
})

test('no request resolves by itself: 24 h of fake clock', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] })
  const p = new Pending()
  const { done } = p.open({ kind: 'question', tool: 'AskUserQuestion', input: {} })
  let settled = false; done.then(() => { settled = true })
  t.mock.timers.tick(24 * 3600 * 1000)
  await new Promise<void>((r) => setImmediate(r))
  assert.equal(settled, false)
  assert.equal(p.list().length, 1)
})
