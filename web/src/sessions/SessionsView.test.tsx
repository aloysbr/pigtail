import { act, fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ROUTES } from '@/api/queries'
import { getUi, setUi, toggleScope } from '@/state/ui'
import { PEERS_ALL, withSessions } from '@/test/peers-fixture'
import { renderApp } from '@/test/render'

/** Opens the web front on the Sessions view (the `s` key) and waits for its groups. */
async function openView(data = withSessions(), opts: Parameters<typeof renderApp>[1] = {}) {
  const r = await renderApp(data, opts)
  fireEvent.keyDown(document.body, { key: 's' })
  return r
}

async function groups() {
  return screen.findByRole('region', { name: 'Sessions' })
}

const row = (sid: string) => document.querySelector<HTMLElement>(`li.srow[data-sid="${sid}"]`)
const rowIds = () => [...document.querySelectorAll<HTMLElement>('li.srow')].map((e) => e.dataset.sid)
const count = (g: string) => screen.getByTestId(`count-${g}`).textContent

describe('Sessions view', () => {
  it('s opens it: serif title « Sessions » in the header, « N vivantes » equal to the sidebar entry (one source, GET /peers)', async () => {
    await openView()
    await groups()
    expect(getUi().view).toBe('sessions')
    const h1 = screen.getByRole('heading', { level: 1, name: 'Sessions' })
    expect(h1.closest('header.d-head')).not.toBeNull()
    const live = PEERS_ALL.filter((p) => p.alive === true).length
    expect(live).toBe(13)
    expect(screen.getByTestId('sess-live')).toHaveTextContent('13 vivantes')
    expect(screen.getByTestId('side-live')).toHaveTextContent('13')
    expect(screen.getByTestId('side-live').textContent).toBe(screen.getByTestId('sess-live').textContent?.replace(' vivantes', ''))
    const nav = screen.getByRole('button', { name: /^Sessions/ })
    expect(nav).toHaveAttribute('aria-current', 'true')
  })

  it('three groups with their counts, the waiting one in an accent pill, then « projet › titre court » blocks', async () => {
    await openView()
    const reg = await groups()
    const heads = within(reg).getAllByRole('heading', { level: 2 }).map((h) => h.querySelector('.gname')?.textContent)
    expect(heads).toEqual(['Attend ta réponse', 'Travaille', 'Inactives'])
    expect([count('wait'), count('work'), count('idle')]).toEqual(['6', '3', '5'])
    expect(screen.getByTestId('count-wait')).toHaveClass('cnt')
    expect(screen.getByTestId('count-wait').closest('.grp-attn')).not.toBeNull()
    expect(screen.getByTestId('count-work').closest('.grp-attn')).toBeNull()
    const blocks = within(reg).getAllByRole('heading', { level: 3 }).map((h) => h.textContent)
    expect(blocks).toContain('pigtail ›Atlas DS3 sessions')
    expect(blocks).toContain('Seedbed ›Watering2 sessions')
    expect(blocks).toContain('Sans sujet')
  })

  it('a waiting row: dot, type chip and reason, state and life written, cell, « depuis »', async () => {
    await openView()
    await groups()
    const r = row('s-idle-arb')!
    expect(r).toHaveClass('sess-wait')
    expect(r.querySelector('.dot')).not.toBeNull()
    expect(r.querySelector('.chip-attn')).toHaveTextContent('Arbitrage')
    expect(r).toHaveTextContent('Maquette t1 : quel style ?')
    expect(r.querySelector('.s-st')).toHaveTextContent('Attend ta réponse · Vivante')
    expect(r.querySelector('.cell')).toHaveTextContent('w3')
    expect(r.querySelector('.s-age')?.textContent).toMatch(/^depuis /)
    // the most severe wait, never waits[0]
    expect(row('s-idle-q-mac')!.querySelector('.chip-attn')).toHaveTextContent('Question')
    expect(row('s-wait-bare')!.querySelector('.chip-attn')).toHaveTextContent('En attente')
  })

  it('the state is written on every row (R9), a paused session says since when', async () => {
    await openView()
    await groups()
    expect(row('s-busy-q')!.querySelector('.s-st')).toHaveTextContent('Travaille · Vivante')
    expect(row('s-shell-bare')!.querySelector('.s-st')).toHaveTextContent('Inactive · Vivante')
    expect(row('s-paused')!.querySelector('.s-st')?.textContent).toMatch(/^Inactive · En pause depuis \d\d:\d\d$/)
    expect(row('s-ended')).toBeNull()
  })

  it('only the rows that wait carry .sess-wait (the accent), and no chip outside them', async () => {
    await openView()
    await groups()
    const wait = new Set(['s-wait-bare', 's-wait-perm', 's-idle-arb', 's-idle-q-mac', 's-idle-limit', 's-wait-review'])
    const marked = [...document.querySelectorAll<HTMLElement>('li.srow.sess-wait')].map((e) => e.dataset.sid)
    expect(new Set(marked)).toEqual(wait)
    expect(document.querySelectorAll('li.srow:not(.sess-wait) .chip-attn')).toHaveLength(0)
  })

  it('a session with no subject, or one the caches do not know, reads « Sans sujet »', async () => {
    await openView()
    await groups()
    const block = (sid: string) => row(sid)!.closest('.sblk')!.querySelector('h3')!.textContent
    expect(block('s-shell-bare')).toMatch(/^Sans sujet/)
    expect(block('s-idle-stale')).toMatch(/^Sans sujet/)
  })

  it('no Modèle, Contexte, Étape workflow column, nor any action button', async () => {
    await openView()
    const reg = await groups()
    expect(reg.textContent).not.toMatch(/Modèle|Contexte|Étape|Open in terminal|Répondre|Opus|Sonnet/)
    expect(reg.querySelectorAll('li.srow button')).toHaveLength(0)
  })

  it('an empty group says « Aucune session. »', async () => {
    const d = withSessions()
    d.peersAll = PEERS_ALL.filter((p) => p.status !== 'busy' && p.session_id !== 's-shell-working')
    await openView(d)
    const reg = await groups()
    expect(count('work')).toBe('0')
    const work = within(reg).getByRole('heading', { level: 2, name: /Travaille/ }).closest('section')!
    expect(within(work).getByText('Aucune session.')).toBeInTheDocument()
  })

  it('loading: the list silhouette, labelled for the sessions', async () => {
    await openView(withSessions(), { hang: [ROUTES.peersAll] })
    expect(await screen.findByLabelText('Chargement des sessions')).toHaveAttribute('aria-busy', 'true')
  })

  it('/peers?all=1 failing: « Sessions indisponibles pour l’instant. » and « Réessayer », which reads again', async () => {
    const r = await openView(withSessions(), { fail: [ROUTES.peersAll] })
    expect(await screen.findByText('Sessions indisponibles pour l’instant.'.replace('’', "'"))).toBeInTheDocument()
    r.setRouteFailing(ROUTES.peersAll, false)
    fireEvent.click(screen.getByRole('button', { name: 'Réessayer' }))
    await groups()
    expect(count('wait')).toBe('6')
  })

  it('a scope pill switched off hides that scope’s sessions and updates the counts; the state is the backlog’s scopesOff', async () => {
    await openView()
    await groups()
    const pills = within(screen.getByRole('group', { name: 'Scopes affichés' }))
    const ts = pills.getByRole('button', { name: /globex/ })
    expect(ts).toHaveAttribute('aria-pressed', 'true')
    expect(ts).toHaveTextContent('1')
    fireEvent.click(ts)
    expect(getUi().scopesOff).toContain('globex')
    expect(row('s-wk-busy')).toBeNull()
    expect(count('work')).toBe('2')
    fireEvent.click(ts)
    expect(row('s-wk-busy')).not.toBeNull()
    expect(count('work')).toBe('3')
  })

  it('the backlog’s toggleScope has the same effect (one scope filter in the app)', async () => {
    await openView()
    await groups()
    act(() => toggleScope('globex', ['perso', 'globex']))
    expect(row('s-wk-busy')).toBeNull()
    expect(count('work')).toBe('2')
    expect(screen.getByRole('button', { name: /globex/ })).toHaveAttribute('aria-pressed', 'false')
  })

  it('/ focuses the filter; the text narrows the rows and the counts; emptying it brings them back', async () => {
    const r = await openView()
    await groups()
    fireEvent.keyDown(document.body, { key: '/' })
    const input = screen.getByRole('searchbox', { name: 'Filtrer les sessions' })
    expect(document.activeElement).toBe(input)
    await r.user.type(input, 'watering')
    expect(rowIds().sort()).toEqual(['s-idle-arb', 's-shell-working', 's-wait-review'])
    expect([count('wait'), count('work')]).toEqual(['2', '1'])
    // an empty group is hidden while a filter is on
    expect(screen.queryByTestId('count-idle')).toBeNull()
    await r.user.clear(input)
    expect(rowIds()).toHaveLength(14)
  })

  it('Esc in the filter clears it, a second Esc (or Esc when empty) gives the focus back to the list and leaves the view open', async () => {
    const r = await openView()
    await groups()
    fireEvent.keyDown(document.body, { key: '/' })
    const input = screen.getByRole('searchbox', { name: 'Filtrer les sessions' })
    await r.user.type(input, 'watering')
    await r.user.keyboard('{Escape}')
    expect(getUi().sessQuery).toBe('')
    expect(document.activeElement).toBe(input)
    await r.user.keyboard('{Escape}')
    expect(document.activeElement).not.toBe(input)
    expect(getUi().view).toBe('sessions')
  })

  it('a filter with no match hides the empty groups, keeps the global empty state, and a cleared filter brings them back', async () => {
    const r = await openView()
    await groups()
    fireEvent.keyDown(document.body, { key: '/' })
    const input = screen.getByRole('searchbox', { name: 'Filtrer les sessions' })
    await r.user.type(input, 'zzzz-no-such')
    expect(rowIds()).toEqual([])
    expect(screen.getAllByText('Aucune session.').length).toBeGreaterThan(0)
    await r.user.clear(input)
    expect(screen.getByTestId('count-idle')).toBeInTheDocument()
  })

  it('a click on a row selects it', async () => {
    await openView()
    await groups()
    fireEvent.click(row('s-busy-q')!)
    expect(getUi().sessionSel).toBe('s-busy-q')
    expect(row('s-busy-q')).toHaveClass('sel')
  })

  it('the sidebar « Backlog » leaves the view', async () => {
    await openView()
    await groups()
    fireEvent.click(screen.getByRole('button', { name: /^Backlog/ }))
    expect(getUi().view).toBe('backlog')
    expect(await screen.findByRole('region', { name: 'Sujets' })).toBeInTheDocument()
    act(() => setUi({ view: 'sessions' }))
    expect(await groups()).toBeInTheDocument()
  })
})
