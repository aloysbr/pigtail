import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Journal } from './core.ts'
import { run } from './main.ts'

const SID = '1da9ff5f-4306-4adf-aa01-d368f0e3e006'
const meta = (over: Record<string, unknown> = {}) => ({
  version: 1 as const, id: 'sdk_1', subject_id: 'sub_1', cwd: '/tmp/w', config_dir: '/tmp/cell', claude: '/bin/claude', mode: 'default',
  prompt: 'reprends le sujet été', session_uuid: SID, host_pid: 0, created_at: 'x', ...over,
})
const ENV = { PATH: '/usr/bin', HOME: '/home/x', CLAUDE_CONFIG_DIR: '/tmp/cell', PIGTAIL_SDK_SESSION: 'sdk_1' }
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

type Script = (ctx: { canUseTool: any; next: () => Promise<string | undefined>; options: any }) => AsyncGenerator<any>
const init = { type: 'system', subtype: 'init', session_id: SID }
const result = { type: 'result', subtype: 'success', result: 'ok', session_id: SID }

function setup(script: Script, over: Record<string, unknown> = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'sdkhost-main-'))
  const journal = new Journal(dir)
  const seen: { options?: any; prompts: any[] } = { prompts: [] }
  let onLine!: (l: string) => Promise<string>
  const query: any = ({ prompt, options }: any) => {
    seen.options = options
    const it = prompt[Symbol.asyncIterator]()
    const next = async () => { const r = await it.next(); if (r.done) return undefined; seen.prompts.push(r.value); return r.value.message.content as string }
    return script({ canUseTool: options.canUseTool, next, options })
  }
  const deps = { query, journal, env: ENV, children: () => [4243], listen: async (_p: string, f: (l: string) => Promise<string>) => { onLine = f } }
  const p = run(meta(over) as any, deps as any)
  const host = () => JSON.parse(readFileSync(join(dir, 'host.json'), 'utf8'))
  const events = () => readFileSync(join(dir, 'events.jsonl'), 'utf8').trim().split('\n').map((l) => JSON.parse(l))
  const cmd = async (o: object) => JSON.parse(await onLine(JSON.stringify(o)))
  const until = async (f: () => boolean) => { for (let i = 0; i < 400; i++) { try { if (f()) return } catch { /* not yet */ } await sleep(5) } throw new Error('timeout') }
  return { p, seen, host, events, cmd, until }
}

async function* twoTurns({ next }: any) {
  yield init
  await next()
  yield result
  while (await next() !== undefined) yield result
}

test('options passed to query: preset, sources, claude path, cwd, mode, session-id, full env', async () => {
  const h = setup(twoTurns)
  await h.until(() => h.seen.options)
  const o = h.seen.options
  assert.deepEqual(o.systemPrompt, { type: 'preset', preset: 'claude_code' })
  assert.deepEqual(o.settingSources, ['user', 'project', 'local'])
  assert.equal(o.pathToClaudeCodeExecutable, '/bin/claude')
  assert.equal(o.cwd, '/tmp/w')
  assert.equal(o.permissionMode, 'default')
  assert.equal(o.extraArgs['session-id'], SID)
  assert.deepEqual(o.env, ENV)
  assert.equal(typeof o.canUseTool, 'function')
  await h.until(() => h.host().state === 'idle')
  await h.cmd({ op: 'end' }); await h.p
})

test('c1d6: accented prompt arrives intact in the first user message', async () => {
  const h = setup(twoTurns)
  await h.until(() => h.seen.prompts.length === 1)
  assert.equal(h.seen.prompts[0].message.content, 'reprends le sujet été')
  assert.equal(h.seen.prompts[0].type, 'user')
  await h.until(() => h.host().state === 'idle')
  await h.cmd({ op: 'end' }); await h.p
})

test('turns: result gives idle and turn 1, host stays; say runs turn 2; say outside idle refused; end exits 0', async () => {
  const h = setup(twoTurns)
  await h.until(() => h.host().state === 'idle')
  assert.equal(h.host().turn, 1)
  assert.equal(h.host().claude_session_id, SID)
  assert.deepEqual(h.host().claude_pids, [4243])
  let done = false; h.p.then(() => { done = true })
  await sleep(30); assert.equal(done, false)
  const ok = await h.cmd({ op: 'say', text: 'encore' })
  assert.equal(ok.ok, true)
  await h.until(() => h.host().turn === 2)
  assert.equal(h.host().state, 'idle')
  assert.equal(h.seen.prompts.at(-1).message.content, 'encore')
  assert.ok(h.events().some((e: any) => e.kind === 'user_message'))
})

test('say outside idle is refused and pushes nothing', async () => {
  let release!: () => void
  const gate = new Promise<void>((r) => { release = r })
  const h = setup(async function* ({ next }: any) { yield init; await next(); await gate; yield result; while (await next() !== undefined) yield result })
  await h.until(() => h.host().state === 'running')
  const r = await h.cmd({ op: 'say', text: 'trop tôt' })
  assert.equal(r.ok, false)
  assert.equal(h.seen.prompts.length, 1)
  release()
  await h.until(() => h.host().state === 'idle')
  await h.cmd({ op: 'end' })
  assert.equal(await h.p, 0)
  assert.equal(h.host().state, 'ended')
})

test('canUseTool AskUserQuestion: waiting, then answer via socket gives running and the SDK gets updatedInput', async () => {
  let got: any
  const input = { questions: [{ question: 'Which color?', options: [{ label: 'Red' }] }] }
  const h = setup(async function* ({ canUseTool, next }: any) {
    yield init; await next()
    got = await canUseTool('AskUserQuestion', input, { toolUseID: 't1' })
    yield result
    while (await next() !== undefined) yield result
  })
  await h.until(() => h.host().state === 'waiting')
  const req = h.host().requests[0]
  assert.equal(req.kind, 'question'); assert.equal(req.tool, 'AskUserQuestion')
  const bad = await h.cmd({ op: 'answer', rid: 'zzz', behavior: 'allow' })
  assert.equal(bad.ok, false)
  const r = await h.cmd({ op: 'answer', rid: req.rid, behavior: 'allow', answers: { 'Which color?': 'Red' } })
  assert.equal(r.ok, true)
  await h.until(() => h.host().state === 'idle')
  assert.deepEqual(got, { behavior: 'allow', updatedInput: { ...input, answers: { 'Which color?': 'Red' } } })
  assert.deepEqual(h.host().requests, [])
  const kinds = h.events().map((e: any) => e.kind)
  assert.ok(kinds.includes('request_opened') && kinds.includes('request_answered'))
  await h.cmd({ op: 'end' }); await h.p
})

test('canUseTool permission: deny reaches the SDK as deny with message', async () => {
  let got: any
  const h = setup(async function* ({ canUseTool, next }: any) {
    yield init; await next()
    got = await canUseTool('Bash', { command: 'ls' }, {})
    yield result
    while (await next() !== undefined) yield result
  })
  await h.until(() => h.host().state === 'waiting')
  assert.equal(h.host().requests[0].kind, 'permission')
  await h.cmd({ op: 'answer', rid: h.host().requests[0].rid, behavior: 'deny', message: 'non' })
  await h.until(() => h.host().state === 'idle')
  assert.deepEqual(got, { behavior: 'deny', message: 'non' })
  await h.cmd({ op: 'end' }); await h.p
})

test('SDK exception: error event, failed, exit 1', async () => {
  const h = setup(async function* ({ next }: any) { yield init; await next(); throw new Error('boom') })
  assert.equal(await h.p, 1)
  assert.equal(h.host().state, 'failed')
  const e = h.events().find((x: any) => x.kind === 'error')
  assert.match(JSON.stringify(e.data), /boom/)
})

test('TestHostWritesEndedWhenClaudeExits: iterator ends by itself, ended, exit 0', async () => {
  const h = setup(async function* ({ next }: any) { yield init; await next(); yield result })
  assert.equal(await h.p, 0)
  assert.equal(h.host().state, 'ended')
  assert.ok(h.events().some((e: any) => e.kind === 'state' && e.data.state === 'ended'))
})

test('rate_limit_event is journaled with resetsAt', async () => {
  const h = setup(async function* ({ next }: any) {
    yield init; await next()
    yield { type: 'rate_limit_event', rate_limit_info: { status: 'allowed', resetsAt: 1790000000, utilization: 0.4 } }
    yield result
    while (await next() !== undefined) yield result
  })
  await h.until(() => h.host().state === 'idle')
  const e = h.events().find((x: any) => x.kind === 'rate_limit')
  assert.equal(e.data.resetsAt, 1790000000)
  await h.cmd({ op: 'end' }); await h.p
})
