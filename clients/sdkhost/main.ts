/** Host of one SDK-held Claude session (node main.ts <dir>); file contract in docs/wire-protocol.md, SDK sessions. */
import { execFileSync } from 'node:child_process'
import { readFileSync, rmSync, existsSync } from 'node:fs'
import { createServer } from 'node:net'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Inbox, Journal, Pending, parseCommand, toPermissionResult, type HostFile, type State } from './core.ts'

export type Meta = {
  version: 1; id: string; subject_id: string; cwd: string; config_dir: string; claude: string
  mode: string; prompt: string; session_uuid: string; host_pid: number; created_at: string
}
export type Deps = {
  query: typeof import('@anthropic-ai/claude-agent-sdk').query
  journal: Journal
  env: NodeJS.ProcessEnv
  children(): number[]
  listen(path: string, onLine: (l: string) => Promise<string>): Promise<void>
}

const userMsg = (text: string) => ({ type: 'user' as const, message: { role: 'user' as const, content: text }, parent_tool_use_id: null })

export async function run(meta: Meta, deps: Deps): Promise<number> {
  const { journal } = deps
  const inbox = new Inbox()
  const pending = new Pending()
  let state: State = 'starting'
  let turn = 0
  let sessionId = ''
  let claudePids: number[] = []

  const sync = () => {
    const h: HostFile = {
      version: 1, host_pid: process.pid, claude_pids: claudePids, claude_session_id: sessionId,
      state, turn, requests: pending.list(), updated_at: new Date().toISOString(),
    }
    journal.writeHost(h)
  }
  const refreshPids = () => { try { claudePids = deps.children() } catch { /* keep the last reading */ } }
  const setState = (s: State) => {
    if (s === state) return
    state = s
    journal.append('state', { state: s })
    refreshPids()
    sync()
  }
  const denyAll = (message: string) => {
    for (const r of pending.list()) pending.answer({ rid: r.rid, behavior: 'deny', message })
  }

  const canUseTool = async (tool: string, input: unknown) => {
    const kind = tool === 'AskUserQuestion' ? 'question' : 'permission'
    const { rid, done } = pending.open({ kind, tool, input })
    const req = pending.list().find((r) => r.rid === rid)!
    journal.append('request_opened', { rid, kind, tool, input })
    setState('waiting')
    sync()
    const a = await done
    journal.append('request_answered', { rid, behavior: a.behavior })
    if (pending.list().length === 0) setState('running')
    else sync()
    return toPermissionResult(req, a)
  }

  const onLine = async (line: string): Promise<string> => {
    const c = parseCommand(line)
    if ('error' in c) return JSON.stringify({ ok: false, error: c.error })
    switch (c.op) {
      case 'status':
        return JSON.stringify({ ok: true, host: { version: 1, host_pid: process.pid, claude_pids: claudePids, claude_session_id: sessionId, state, turn, requests: pending.list() } })
      case 'answer':
        return JSON.stringify(pending.answer(c.answer) ? { ok: true } : { ok: false, error: 'unknown or already answered request' })
      case 'say':
        if (state !== 'idle') return JSON.stringify({ ok: false, error: `not idle (state ${state})` })
        journal.append('user_message', { text: c.text })
        setState('running')
        inbox.push(c.text)
        return JSON.stringify({ ok: true })
      case 'end':
        denyAll('session ended')
        inbox.close()
        return JSON.stringify({ ok: true })
    }
  }

  sync()
  await deps.listen(join(journal.dir, 'host.sock'), onLine)
  inbox.push(meta.prompt)

  try {
    const q = deps.query({
      prompt: (async function* () { for await (const t of inbox) yield userMsg(t) })() as any,
      options: {
        systemPrompt: { type: 'preset', preset: 'claude_code' },
        settingSources: ['user', 'project', 'local'],
        pathToClaudeCodeExecutable: meta.claude,
        cwd: meta.cwd,
        permissionMode: meta.mode as any,
        extraArgs: { 'session-id': meta.session_uuid },
        env: deps.env,
        canUseTool: canUseTool as any,
      },
    })
    for await (const m of q as AsyncIterable<any>) {
      if (m.type === 'system' && m.subtype === 'init') {
        sessionId = m.session_id ?? sessionId
        journal.append('init', { session_id: m.session_id })
        refreshPids()
        if (state === 'starting') setState('running'); else sync()
      } else if (m.type === 'assistant') {
        for (const c of m.message?.content ?? []) {
          if (c.type === 'text') journal.append('assistant_text', { text: c.text })
          else if (c.type === 'tool_use') journal.append('tool_use', { id: c.id, name: c.name, input: c.input })
        }
      } else if (m.type === 'user') {
        for (const c of Array.isArray(m.message?.content) ? m.message.content : []) {
          if (c?.type === 'tool_result') journal.append('tool_result', { tool_use_id: c.tool_use_id, content: c.content, is_error: c.is_error })
        }
      } else if (m.type === 'rate_limit_event') {
        journal.append('rate_limit', m.rate_limit_info ?? {})
      } else if (m.type === 'result') {
        turn++
        journal.append('result', { subtype: m.subtype, result: m.result, is_error: m.is_error })
        setState('idle')
        sync()
      }
    }
    // The iterator ended: `end` closed the input, or claude exited by itself.
    inbox.close()
    denyAll('session ended')
    setState('ended')
    return 0
  } catch (e) {
    journal.append('error', { message: e instanceof Error ? e.message : String(e) })
    inbox.close()
    denyAll('host failed')
    setState('failed')
    return 1
  }
}

/** Direct children of this process that look like claude (H9, method of spikes/sdk/host.ts). */
function directChildren(): number[] {
  const out = execFileSync('/bin/ps', ['-axo', 'pid=,ppid=,command='], { encoding: 'utf8' })
  const rows = out.split('\n').map((l) => l.trim().match(/^(\d+)\s+(\d+)\s+(.*)$/)).filter(Boolean) as RegExpMatchArray[]
  return rows.filter((m) => Number(m[2]) === process.pid && /claude/.test(m[3])).map((m) => Number(m[1]))
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const dir = process.argv[2]
  const meta = JSON.parse(readFileSync(join(dir, 'meta.json'), 'utf8')) as Meta
  const { query } = await import('@anthropic-ai/claude-agent-sdk')
  let server: ReturnType<typeof createServer> | undefined
  const listen = (path: string, onLine: (l: string) => Promise<string>) => new Promise<void>((resolve, reject) => {
    if (existsSync(path)) rmSync(path)
    server = createServer((c) => {
      let buf = ''
      c.on('data', async (d) => {
        buf += d.toString()
        let i
        while ((i = buf.indexOf('\n')) >= 0) {
          const line = buf.slice(0, i); buf = buf.slice(i + 1)
          const reply = await onLine(line)
          try { c.write(reply + '\n') } catch { /* client gone */ }
        }
      })
      c.on('error', () => {})
    })
    server.once('error', reject)
    server.listen(path, () => resolve())
  })
  const code = await run(meta, { query, journal: new Journal(dir), env: process.env, children: directChildren, listen })
  server?.close()
  rmSync(join(dir, 'host.sock'), { force: true })
  process.exit(code)
}
