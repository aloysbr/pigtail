import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { LogEntry } from '@/api/types'
import { openPanel, setUi } from '@/state/ui'
import { makeBacklog, type BacklogData } from '@/test/backlog'
import { renderApp, type Reply, type WriteCall } from '@/test/render'
import { resetToasts } from '@/ui/toasts'

afterEach(() => resetToasts())

let seq = 0
function logLine(subject: string, text: string): LogEntry {
  seq += 1
  return { id: `log${seq}`, subject_id: subject, ts: new Date(Date.now() + seq).toISOString(), kind: 'status', text }
}

/** The daemon's POST /subjects/{id} for the web front over the test data: writes the status, logs the change. */
function statusRoute(data: () => BacklogData, id: string) {
  return (req: WriteCall): Reply => {
    const s = data().subjects.find((x) => x.id === id)
    if (!s) return { status: 404, body: { error: { code: 'NOT_FOUND', message: 'no' } } }
    const { status } = req.body as { status: string }
    const d = data()
    d.logs = { ...d.logs, [id]: [logLine(id, `${s.status} → ${status}`), ...(d.logs?.[id] ?? [])] }
    s.status = status
    return { status: 200, body: { data: s } }
  }
}

const IDS = ['pig-r1', 'pig-r2', 'pig-p1', 'pig-p2', 'pig-live', 'pig-d1', 'pig-x1']

async function setup(data: BacklogData = makeBacklog()) {
  const h = await renderApp(data)
  for (const id of IDS) h.setRoute(`POST /subjects/${id}`, statusRoute(h.data, id))
  return h
}

async function press(k: string, target: Element = document.body) {
  await act(async () => {
    fireEvent.keyDown(target, { key: k })
  })
}

function select(id: string) {
  act(() => setUi({ sel: id }))
}

const posts = (w: WriteCall[]) => w.filter((c) => c.method === 'POST')

function status(): HTMLElement {
  const toasts = screen.getAllByRole('status').filter((x) => x.classList.contains('toast6'))
  if (toasts.length !== 1) throw new Error(`toasts: ${toasts.length}`)
  return toasts[0]
}

describe('Standby / Reprendre: h', () => {
  it('h on an Ouvert writes parked and nothing else (no session touched); toast D9-2', async () => {
    const h = await setup()
    select('pig-r2')
    await press('h')
    await waitFor(() => expect(posts(h.writes())).toHaveLength(1))
    expect(h.writes()).toEqual([{ method: 'POST', url: '/subjects/pig-r2', body: { status: 'parked' } }])
    await waitFor(() => expect(status()).toHaveTextContent('« Atlas palette » en standby'))
    expect(status()).not.toHaveTextContent('session')
  })

  it('h on a live subject: « « X » en standby · la session X reste ouverte » (D9-2)', async () => {
    const data = makeBacklog()
    data.peers = [{ session_id: 'sess-1', subject_id: 'pig-live', scope: 'perso', alive: true, cell_id: 'mp' }]
    const h = await setup(data)
    select('pig-live')
    await press('h')
    await waitFor(() => expect(posts(h.writes())).toHaveLength(1))
    expect(h.writes()[0].body).toEqual({ status: 'parked' })
    await waitFor(() => expect(status()).toHaveTextContent('« Session relay » en standby · la session mp reste ouverte'))
  })

  it('h on a Standby with « building → parked » in its log writes building; toast D9-3', async () => {
    const data = makeBacklog()
    data.logs = { 'pig-p1': [logLine('pig-p1', 'building → parked'), logLine('pig-p1', 'inbox → building')] }
    const h = await setup(data)
    select('pig-p1')
    await press('h')
    await waitFor(() => expect(posts(h.writes())).toHaveLength(1))
    expect(h.writes()[0]).toEqual({ method: 'POST', url: '/subjects/pig-p1', body: { status: 'building' } })
    await waitFor(() => expect(status()).toHaveTextContent('« Parked one » repris · Ouvert (building)'))
  })

  it('h on a Standby with no log writes inbox', async () => {
    const h = await setup()
    select('pig-p2')
    await press('h')
    await waitFor(() => expect(posts(h.writes())).toHaveLength(1))
    expect(h.writes()[0].body).toEqual({ status: 'inbox' })
  })

  it('h on Done or Abandonné: no call', async () => {
    const h = await setup()
    for (const id of ['pig-d1', 'pig-x1', 'pig-c1']) {
      select(id)
      await press('h')
    }
    expect(h.writes()).toEqual([])
  })

  it('h while typing in a field does nothing', async () => {
    const h = await setup()
    select('pig-r2')
    const search = screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })
    search.focus()
    await press('h', search)
    expect(h.writes()).toEqual([])
  })

  it('a 403: the screen comes back and the toast says why', async () => {
    const h = await setup()
    h.setRoute('POST /subjects/pig-r2', () => ({ status: 403, body: { error: { code: 'FORBIDDEN_FOR_WEB', message: 'no' } } }))
    select('pig-r2')
    await press('h')
    await waitFor(() => expect(status()).toHaveTextContent("Pigtail n'a pas le droit de faire ce changement sur « Atlas palette »."))
    expect(h.qc.getQueryData<{ id: string; status: string }[]>(['subjects'])?.find((s) => s.id === 'pig-r2')?.status).toBe('designing')
  })

  it('a 423: the screen comes back with the maintenance toast', async () => {
    const h = await setup()
    h.setRoute('POST /subjects/pig-r2', () => ({ status: 423, body: { error: { code: 'MAINTENANCE_FROZEN', message: 'frozen' } } }))
    select('pig-r2')
    await press('h')
    await waitFor(() => expect(status()).toHaveTextContent('Maintenance en cours : réessaie quand elle est finie.'))
    expect(h.qc.getQueryData<{ id: string; status: string }[]>(['subjects'])?.find((s) => s.id === 'pig-r2')?.status).toBe('designing')
  })

  it('z undoes: the status before is written back, « Annulé · « X » rétabli en Ouvert » (D9-10)', async () => {
    const h = await setup()
    select('pig-r2')
    await press('h')
    await waitFor(() => expect(status()).toHaveTextContent('« Atlas palette » en standby'))
    await press('z')
    await waitFor(() => expect(posts(h.writes())).toHaveLength(2))
    expect(posts(h.writes())[1]).toEqual({ method: 'POST', url: '/subjects/pig-r2', body: { status: 'designing' } })
    await waitFor(() => expect(status()).toHaveTextContent('Annulé · « Atlas palette » rétabli en Ouvert'))
  })

  it('the status changed at the CLI between the action and z: toast D9-9, no POST (read from GET /subjects/{id}, not the cache)', async () => {
    const h = await setup()
    select('pig-r2')
    await press('h')
    await waitFor(() => expect(status()).toHaveTextContent('« Atlas palette » en standby'))
    // a CLI changes it behind the web front's back; the cache still says parked
    const s = h.data().subjects.find((x) => x.id === 'pig-r2')
    if (s) s.status = 'building'
    await press('z')
    await waitFor(() => expect(status()).toHaveTextContent("Impossible d'annuler : « Atlas palette » a changé depuis."))
    expect(posts(h.writes())).toHaveLength(1)
    expect(h.fetchFn.mock.calls.some(([u]) => String(u) === '/subjects/pig-r2')).toBe(true)
  })
})

describe('Standby / Reprendre: the sheet', () => {
  it('« Cycle de vie » carries the ⏸ switch on an Ouvert; a click parks it; the log entry shows once re-read', async () => {
    const h = await setup()
    act(() => openPanel('pig-r2'))
    const box = await screen.findByRole('group', { name: /Cycle de vie|Position/ })
    const sw = within(box).getByRole('button', { name: 'Mettre en standby' })
    expect(sw).toHaveAttribute('aria-keyshortcuts', 'h')
    await act(async () => {
      fireEvent.click(sw)
    })
    await waitFor(() => expect(h.writes()).toEqual([{ method: 'POST', url: '/subjects/pig-r2', body: { status: 'parked' } }]))
    // the log is invalidated and re-read: the history shows the entry written
    await waitFor(() => expect(document.querySelector('.lpanel')).toHaveTextContent('designing → parked'))
    await waitFor(() => expect(within(screen.getByRole('group', { name: /Position/ })).getByRole('button', { name: 'Reprendre' })).toBeInTheDocument())
  })

  it('Standby: ▶ Reprendre; En cours: ⏸; Done, Abandonné and a capture: no control', async () => {
    const data = makeBacklog()
    await setup(data)
    act(() => openPanel('pig-p1'))
    expect(await screen.findByRole('button', { name: 'Reprendre' })).toBeInTheDocument()
    act(() => openPanel('pig-live'))
    expect(await screen.findByRole('button', { name: 'Mettre en standby' })).toBeInTheDocument()
    act(() => setUi({ showDropped: true }))
    const titles: Record<string, string> = { 'pig-d1': 'Done atlas', 'pig-x1': 'Dropped one', 'pig-c1': 'Captured one' }
    for (const id of ['pig-d1', 'pig-x1', 'pig-c1']) {
      act(() => openPanel(id))
      await waitFor(() => expect(document.querySelector('#lp-title')).toHaveTextContent(titles[id]))
      expect(screen.getByRole('group', { name: /Position/ })).toBeInTheDocument()
      const sheet = within(document.querySelector<HTMLElement>('.lpanel') as HTMLElement)
      expect(sheet.queryByRole('button', { name: /standby|Reprendre/i })).toBeNull()
      expect(sheet.queryByRole('button', { name: /^Done/ })).toBeNull()
      expect(screen.queryByRole('menuitem', { name: /Done/ })).toBeNull()
      expect(screen.queryByRole('menuitemradio', { name: /^Done/ })).toBeNull()
    }
  })

  it('En cours over a stored parked: « Standby » in the box with the tooltip « Statut stocké : parked » (D10)', async () => {
    const data = makeBacklog()
    const live = data.subjects.find((x) => x.id === 'pig-live')
    if (live) live.status = 'parked'
    await setup(data)
    act(() => openPanel('pig-live'))
    const box = await screen.findByRole('group', { name: /Position/ })
    expect(within(box).getByText('Standby').closest('[title]')).toHaveAttribute('title', 'Statut stocké : parked')
    expect(within(box).getByRole('button', { name: 'Reprendre' })).toBeInTheDocument()
  })
})
