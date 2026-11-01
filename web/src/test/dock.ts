import { screen } from '@testing-library/react'
import type { OutputGroup, Peer, Wait } from '@/api/types'
import { READER_KEEP } from '@/reader/Reader'
import { makeBacklog, minutesAgo, type BacklogData } from './backlog'
import { groupButton, renderApp } from './render'

// The session dock's fixtures (lot 4: SessionDock.test.tsx, NoSession.test.tsx): pig-r1's mockup in the reader, its waits and sessions.

const ROOT = '/Users/x/_outputs/outputs-consultables/mockup'
const SERVED = 'http://127.0.0.1:7801'

export function group(): OutputGroup {
  const ts = Date.now() - 3_600_000
  return {
    key: ROOT,
    name: 'outputs-consultables',
    kind: 'mockup',
    first_seq: 1,
    tours: [1, 2].map((n) => ({ n, label: `t${n}`, output_id: `out_t${n}`, title: `mockup t${n}`, ts: ts + n, path: `${ROOT}/t${n}/index.html`, exists: true, served_url: `${SERVED}${ROOT}/t${n}/` })),
  }
}

/** An open arbitrage of pig-r1 by `session`, opened `min` minutes ago. */
export function arb(extra: Partial<Wait> = {}): Wait {
  return {
    id: 'wait_open',
    session_id: 'sess-w',
    session_label: 'Atlas web',
    subject_id: 'pig-r1',
    type: 'arbitrage',
    source: 'declared',
    state: 'open',
    reason: 'Calque : plein écran ou fenêtre ?',
    opened_at: minutesAgo(5),
    details: { options: [{ id: 'A', label: 'Plein écran', reco: true }, { id: 'B', label: 'Fenêtre' }] },
    ...extra,
  }
}

/** A closed, answered wait (one exchange) of `session`, closed `min` minutes ago. */
export function past(id: string, session: string, min: number, reason: string, text: string, subject = 'pig-r1'): Wait {
  return {
    id,
    session_id: session,
    subject_id: subject,
    type: 'question',
    source: 'declared',
    state: 'answered',
    reason,
    opened_at: minutesAgo(min + 2),
    closed_at: minutesAgo(min),
    answer: { text, at: minutesAgo(min) },
  }
}

export function peer(id: string, extra: Partial<Peer> = {}): Peer {
  return { session_id: id, subject_id: 'pig-r1', scope: 'perso', alive: true, status: 'busy', cell_id: 'mp', updated_at: minutesAgo(1), ...extra }
}

/** The app with pig-r1's mockup open in the reader; returns the dialog and its dock. */
export async function openDock(prep: (d: BacklogData) => void = () => {}, width = 1440) {
  const data = makeBacklog()
  data.groups = { 'pig-r1': [group()] }
  data.outputs = { 'pig-r1': [] }
  data.waits = []
  prep(data)
  const c = await renderApp(data, { width })
  await c.user.click(groupButton('th:th_pig'))
  await c.user.click(document.querySelector('li[data-id="pig-r1"]') as HTMLElement)
  await c.user.click(await screen.findByRole('button', { name: 'outputs-consultables' }))
  const dlg = await screen.findByRole('dialog', { name: /Maquette outputs-consultables/ })
  const dock = dlg.querySelector<HTMLElement>(READER_KEEP)
  if (!dock) throw new Error('no session dock in the reader')
  return { ...c, dlg, dock }
}

