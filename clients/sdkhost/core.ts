// Pure core of the SDK host: protocol, journal, pending requests. No SDK import.
// File contract: docs/wire-protocol.md § SDK sessions.
import { appendFileSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

export type State = 'starting' | 'running' | 'waiting' | 'idle' | 'ended' | 'failed' | 'lost'
export type Event = {
  seq: number
  at: string
  kind: 'init' | 'assistant_text' | 'tool_use' | 'tool_result' | 'request_opened' | 'request_answered' | 'user_message' | 'result' | 'state' | 'error' | 'rate_limit'
  data: unknown
}
export type Request = { rid: string; kind: 'question' | 'permission'; tool: string; input: unknown; opened_at: string }
export type Answer = { rid: string; behavior: 'allow' | 'deny'; answers?: Record<string, string>; message?: string }
export type HostFile = {
  version: 1
  host_pid: number
  claude_pids: number[]
  claude_session_id: string
  state: State
  turn: number
  requests: Request[]
  updated_at: string
}

export class Journal {
  private seq = 0
  readonly dir: string
  constructor(dir: string) {
    this.dir = dir
    mkdirSync(dir, { recursive: true })
    const f = join(dir, 'events.jsonl')
    if (existsSync(f)) {
      for (const line of readFileSync(f, 'utf8').split('\n')) {
        if (!line.trim()) continue
        try {
          const e = JSON.parse(line)
          if (typeof e.seq === 'number' && e.seq > this.seq) this.seq = e.seq
        } catch { /* a torn last line is ignored */ }
      }
    }
  }
  append(kind: Event['kind'], data: unknown): Event {
    const e: Event = { seq: ++this.seq, at: new Date().toISOString(), kind, data }
    appendFileSync(join(this.dir, 'events.jsonl'), JSON.stringify(e) + '\n')
    return e
  }
  writeHost(h: HostFile): void {
    const tmp = join(this.dir, 'host.json.tmp')
    writeFileSync(tmp, JSON.stringify(h))
    renameSync(tmp, join(this.dir, 'host.json'))
  }
}

export class Pending {
  private n = 0
  private m = new Map<string, { req: Request; resolve: (a: Answer) => void }>()
  open(r: Omit<Request, 'rid' | 'opened_at'>): { rid: string; done: Promise<Answer> } {
    const rid = `r${++this.n}`
    const req: Request = { ...r, rid, opened_at: new Date().toISOString() }
    let resolve!: (a: Answer) => void
    const done = new Promise<Answer>((res) => { resolve = res })
    this.m.set(rid, { req, resolve })
    return { rid, done }
  }
  answer(a: Answer): boolean {
    const e = this.m.get(a.rid)
    if (!e) return false
    this.m.delete(a.rid)
    e.resolve(a)
    return true
  }
  list(): Request[] { return [...this.m.values()].map((e) => e.req) }
}

// Async input of the turns: the session stays open between turns.
export class Inbox implements AsyncIterable<string> {
  private q: string[] = []
  private w: ((v: IteratorResult<string>) => void) | null = null
  private done = false
  push(text: string): void {
    if (this.done) return
    if (this.w) { const w = this.w; this.w = null; w({ value: text, done: false }) } else this.q.push(text)
  }
  close(): void {
    this.done = true
    if (this.w) { const w = this.w; this.w = null; w({ value: undefined, done: true }) }
  }
  [Symbol.asyncIterator](): AsyncIterator<string> {
    return {
      next: () => {
        if (this.q.length) return Promise.resolve({ value: this.q.shift() as string, done: false })
        if (this.done) return Promise.resolve({ value: undefined, done: true })
        return new Promise<IteratorResult<string>>((r) => { this.w = r })
      },
    }
  }
}

export function toPermissionResult(req: Request, a: Answer): { behavior: 'allow'; updatedInput: unknown } | { behavior: 'deny'; message: string } {
  if (a.behavior === 'deny') return { behavior: 'deny', message: a.message ? a.message : 'refusé' }
  if (a.answers) return { behavior: 'allow', updatedInput: { ...(req.input as object), answers: a.answers } }
  return { behavior: 'allow', updatedInput: req.input }
}

type Command = { op: 'answer'; answer: Answer } | { op: 'say'; text: string } | { op: 'status' } | { op: 'end' } | { error: string }

export function parseCommand(line: string): Command {
  let m: any
  try { m = JSON.parse(line) } catch { return { error: 'invalid json' } }
  if (!m || typeof m !== 'object' || Array.isArray(m)) return { error: 'not an object' }
  switch (m.op) {
    case 'status': return { op: 'status' }
    case 'end': return { op: 'end' }
    case 'say':
      return typeof m.text === 'string' ? { op: 'say', text: m.text } : { error: 'say needs text' }
    case 'answer': {
      if (typeof m.rid !== 'string' || !m.rid) return { error: 'answer needs rid' }
      if (m.behavior !== 'allow' && m.behavior !== 'deny') return { error: 'behavior must be allow or deny' }
      const a: Answer = { rid: m.rid, behavior: m.behavior }
      if (typeof m.message === 'string') a.message = m.message
      if (m.answers && typeof m.answers === 'object' && !Array.isArray(m.answers)) {
        const out: Record<string, string> = {}
        for (const [k, v] of Object.entries(m.answers)) {
          // multiSelect: chosen labels joined by ", " (measured H10)
          if (Array.isArray(v) && v.every((x) => typeof x === 'string')) out[k] = v.join(', ')
          else if (typeof v === 'string') out[k] = v
          else return { error: `answers[${k}] must be a string or a string list` }
        }
        a.answers = out
      }
      return { op: 'answer', answer: a }
    }
    default: return { error: `unknown op ${String(m.op)}` }
  }
}
