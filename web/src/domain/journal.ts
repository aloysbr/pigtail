import type { LogEntry } from '@/api/types'

/** The header of the events no rule places. */
export const NO_STEP = 'Sans étape'

export type EventType = 'wait' | 'decision' | 'out' | 'status' | 'session' | 'idea'

export interface Event {
  id: string
  type: EventType
  /** « HH:MM », local to the tz. */
  time: string
  main: string
  detail?: string
  /** « t<n> », only when the step has two rounds or more. */
  round?: string
  /** The events of the same minute and step folded behind this one, most telling first. */
  fold: Event[]
}

export interface Step {
  /** The step name a line carries (`step=<name>` or `<name> →`), or '' for « Sans étape ». */
  id: string
  name: string
  current: boolean
  /** « HH:MM – HH:MM », oldest – newest; one time when they match. */
  span: string
  /** « t1 → t3 » when the step has two rounds or more. */
  rounds?: string
  events: Event[]
}

export interface Day {
  /** YYYY-MM-DD, local to the tz. */
  key: string
  label: string
  count: number
  steps: Step[]
}

/** The most telling first: the head of a fold (out before wait, session before status). */
const RANK: Record<EventType, number> = { decision: 0, out: 1, wait: 2, session: 3, status: 4, idea: 5 }

/**
 * Rule 2: a word, then at most 24 characters without `→`, then `→` (`review →`, `sketch r1 →`, `draft written →`). The
 * vocabulary is the log's own: no step list is known in advance.
 */
const ARROW = /^([a-z][\w-]*)(?=\s|→)([^→]{0,24})→\s*/
const STEP_TOKEN = /(?:^|\s)step=([a-z][\w-]*)/
const SESSION_END = /^session-end\b[^·]*·\s*/
const WORKSPACE = /^workspace\s+(\S+)\s+(\S+)\s+lié(?=\s|$)(.*)$/s
const ROUND = /\bround=(\d+)\b/
const ROUND_T = /\bt(\d+)\b/
/** A path ending in a file name, reduced to the name (`notes/…/draft.md` → `draft.md`). */
const PATH = /\S*\/([^\s/]+\.[A-Za-z0-9]+)(?=[\s,;)]|$)/g

interface Parts {
  key: string
  minute: string
  time: string
  year: string
  weekday: string
  day: string
  month: string
}

function partsOf(ms: number, tz?: string): Parts {
  const f = new Intl.DateTimeFormat('fr-FR', {
    timeZone: tz,
    year: 'numeric',
    month: 'long',
    day: 'numeric',
    weekday: 'long',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  })
  const p: Record<string, string> = {}
  for (const x of f.formatToParts(new Date(ms))) p[x.type] = x.value
  const n = new Intl.DateTimeFormat('en-CA', { timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit' }).format(
    new Date(ms),
  )
  const time = `${p.hour}:${p.minute}`
  return { key: n, minute: `${n} ${time}`, time, year: p.year, weekday: p.weekday, day: p.day, month: p.month }
}

interface Rec {
  e: LogEntry
  ms: number
  p: Parts
  arrow: RegExpExecArray | null
  step: string | null
  type: EventType
  main: string
  detail?: string
  roundN?: number
}

function stepOf12(r: Rec): string | null {
  const tok = STEP_TOKEN.exec(r.e.text)
  if (tok) return tok[1]
  return r.arrow ? r.arrow[1] : null
}

/** Places every record on a step, by rules 1 to 4, in that order. */
function placeSteps(recs: Rec[]): void {
  const asc = [...recs].sort((a, b) => a.ms - b.ms)
  const r12 = new Map<Rec, string | null>(asc.map((r) => [r, stepOf12(r)]))
  const byMinute = new Map<string, Rec[]>()
  for (const r of asc) byMinute.set(r.p.minute, [...(byMinute.get(r.p.minute) ?? []), r])
  const sameMinute = (r: Rec, f: (o: Rec) => string | null): string | null => {
    for (const o of byMinute.get(r.p.minute) ?? []) {
      if (o === r) continue
      const s = f(o)
      if (s) return s
    }
    return null
  }
  // rules 1-2, then 3 (same minute as a 1-2), then 4 (nearest strictly earlier placed event)
  asc.forEach((r, i) => {
    let s = r12.get(r) ?? sameMinute(r, (o) => r12.get(o) ?? null)
    if (!s) {
      for (let j = i - 1; j >= 0; j--) {
        if (asc[j].ms < r.ms && asc[j].step) {
          s = asc[j].step
          break
        }
      }
    }
    r.step = s ?? ''
  })
}

/** Only a « saved » line (`write saved → path`) is reduced to a file name; other steps keep their path. */
const PLAN_WRITTEN = /^\S+ saved\s*→/

function reducePaths(s: string): string {
  return s.replace(PATH, '$1')
}

/** « <name> → Project Alpha [answer wait_…]: B · Fenêtre » (internal/daemon/handlers/waits_answer.go traceAnswer); the name is the configured human. */
const ANSWER = /^[^→\n]{1,64} → (.+?) \[answer [^\]]*\]:?\s*(.*)$/s

/** Type, main text and detail of a record (its step already placed). */
function describe(r: Rec): void {
  const { text, kind } = r.e
  const se = SESSION_END.exec(text)
  if (se) {
    const rest = text.slice(se[0].length).trim()
    r.type = /en attente|attend|validation avant/.test(rest) ? 'wait' : 'session'
    const segs = rest.split(' ; ')
    let main = segs.length > 1 ? segs.slice(1).join(' ; ') : segs[0]
    let detail = segs.length > 1 ? segs[0] : undefined
    if (r.type === 'session') main = `Fin de session · ${main}`
    else if (detail) detail = `Fin de session · ${detail}`
    r.main = main
    r.detail = detail
    return
  }
  const ws = WORKSPACE.exec(text)
  if (ws) {
    r.type = 'session'
    r.main = `workspace ${ws[1]} lié${ws[3]}`.trim()
    r.detail = ws[2]
    return
  }
  if (r.arrow) {
    r.type = /validée|décidé/.test(text) ? 'decision' : 'out'
    const rest = text.slice(r.arrow[0].length).trim()
    r.main = PLAN_WRITTEN.test(text) ? reducePaths(rest) : rest
    return
  }
  if (kind === 'handoff' || kind === 'handoff-auto' || kind === 'delivery') {
    r.type = 'out'
    const lines = text
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
    r.main = (lines[0] ?? '').replace(/^#+\s*/, '')
    if (lines[1]) r.detail = lines[1].replace(/\*\*/g, '')
    return
  }
  if (kind === 'wait') {
    // The human's answer, as the daemon logs it: « <name> → <to> [answer <wait id>]: <body> »; the ids stay out of the line
    const ans = ANSWER.exec(text)
    r.type = 'wait'
    if (ans) {
      r.main = ans[1].startsWith('kept for the subject') ? 'Réponse gardée pour la prochaine session du sujet' : `Réponse envoyée à ${ans[1]}`
      if (ans[2]) r.detail = ans[2]
    } else r.main = text
    return
  }
  r.type =
    kind === 'status' || kind === 'phase'
      ? 'status'
      : kind === 'progress'
        ? 'session'
        : 'idea' // idea, note, and any kind the daemon adds later
  r.main = text
}

function roundOf(text: string): number | undefined {
  const m = ROUND.exec(text) ?? ROUND_T.exec(text)
  return m ? Number(m[1]) : undefined
}

function span(times: string[]): string {
  const s = [...times].sort()
  return s[0] === s[s.length - 1] ? s[0] : `${s[0]} – ${s[s.length - 1]}`
}

function plural(n: number): string {
  return `${n} ${n > 1 ? 'événements' : 'événement'}`
}

/** Internal wait ids (« wait_<hex> ») never reach the reader, nor the brackets or spaces they leave behind. */
function stripWaitIds(s: string): string {
  return s
    .replace(/\s*[([]\s*(?:answer\s+)?wait_[0-9a-f]+\s*[)\]]/gi, '')
    .replace(/\bwait_[0-9a-f]+\b/gi, '')
    .replace(/[ \t]{2,}/g, ' ')
    .trim()
}

function toEvent(r: Rec, showRounds: boolean, fold: Event[]): Event {
  const ev: Event = { id: r.e.id, type: r.type, time: r.p.time, main: stripWaitIds(r.main), fold }
  if (r.detail) ev.detail = stripWaitIds(r.detail)
  if (showRounds && r.roundN !== undefined) ev.round = `t${r.roundN}`
  return ev
}

/**
 * The subject's journal grouped by day (newest first), then workflow step (newest first), then typed events (newest
 * first), events of the same minute and step folded behind the most telling one. Pure: `now` and `tz` are injected.
 */
export function buildJournal(entries: LogEntry[], now: Date, current?: string, tz?: string): Day[] {
  const recs: Rec[] = []
  for (const e of entries) {
    const ms = Date.parse(e.ts)
    if (Number.isNaN(ms)) continue
    recs.push({ e, ms, p: partsOf(ms, tz), arrow: e.kind === 'status' || (e.kind === 'wait' && ANSWER.test(e.text)) || SESSION_END.test(e.text) ? null : ARROW.exec(e.text), step: null, type: 'idea', main: '' })
  }
  if (!recs.length) return []
  placeSteps(recs)
  for (const r of recs) describe(r)

  // rounds: own token, else one of the same minute and step
  for (const r of recs) r.roundN = roundOf(r.e.text)
  for (const r of recs) {
    if (r.roundN !== undefined) continue
    const o = recs.find((x) => x !== r && x.step === r.step && x.p.minute === r.p.minute && roundOf(x.e.text) !== undefined)
    if (o) r.roundN = roundOf(o.e.text)
  }

  const todayKey = partsOf(now.getTime(), tz).key
  const nowYear = partsOf(now.getTime(), tz).year
  const days = new Map<string, Rec[]>()
  for (const r of recs) days.set(r.p.key, [...(days.get(r.p.key) ?? []), r])

  let currentShown = false
  return [...days.entries()]
    .sort((a, b) => (a[0] < b[0] ? 1 : -1))
    .map(([key, list]) => {
      const p = list[0].p
      const date = `${p.weekday} ${p.day} ${p.month}${p.year !== nowYear ? ` ${p.year}` : ''}`
      const label = `${key === todayKey ? "Aujourd'hui · " : ''}${date} · ${plural(list.length)}`

      const bySteps = new Map<string, Rec[]>()
      for (const r of list) bySteps.set(r.step ?? '', [...(bySteps.get(r.step ?? '') ?? []), r])
      const steps: Step[] = [...bySteps.entries()]
        .map(([id, rs]) => {
          rs.sort((a, b) => b.ms - a.ms)
          const distinct = [...new Set(rs.map((r) => r.roundN).filter((n): n is number => n !== undefined))]
          const showRounds = distinct.length >= 2
          // fold: same minute, behind the most telling (tie: the newest)
          const groups = new Map<string, Rec[]>()
          for (const r of rs) groups.set(r.p.minute, [...(groups.get(r.p.minute) ?? []), r])
          const heads: { r: Rec; fold: Rec[] }[] = []
          for (const g of groups.values()) {
            const sorted = [...g].sort((a, b) => RANK[a.type] - RANK[b.type] || b.ms - a.ms)
            heads.push({ r: sorted[0], fold: sorted.slice(1) })
          }
          heads.sort((a, b) => b.r.ms - a.r.ms)
          const isCurrent = !!current && id === current && !currentShown
          if (isCurrent) currentShown = true
          const step: Step = {
            id,
            name: id || NO_STEP,
            current: isCurrent,
            span: span(rs.map((r) => r.p.time)),
            events: heads.map((h) =>
              toEvent(
                h.r,
                showRounds,
                h.fold.map((f) => toEvent(f, showRounds, [])),
              ),
            ),
          }
          if (showRounds) step.rounds = `t${Math.min(...distinct)} → t${Math.max(...distinct)}`
          return { step, newest: rs[0].ms }
        })
        .sort((a, b) => b.newest - a.newest)
        .map((x) => x.step)
      return { key, label, count: list.length, steps }
    })
}
