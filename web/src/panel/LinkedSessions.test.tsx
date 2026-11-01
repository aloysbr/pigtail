import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { PEERS_ALL_KEY } from '@/api/sessions'
import { getUi, openPanel } from '@/state/ui'
import { PEERS_ALL, withSessions } from '@/test/peers-fixture'
import { renderApp } from '@/test/render'

const sheet = () => document.querySelector<HTMLElement>('.lpanel')!
const linked = () => screen.queryByRole('region', { name: /^Sessions liées/ })
const ids = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>('li.srow')].map((e) => e.dataset.sid)
const allReads = (f: { mock: { calls: unknown[][] } }) => f.mock.calls.filter(([u, init]) => String(u) === '/peers?all=1' && !(init as RequestInit | undefined)?.method).length

describe('Sessions liées on the subject sheet', () => {
  it('lists the visible sessions of the subject, compact, waiting first, then working, then resting; « Sessions liées · N »', async () => {
    await renderApp(withSessions())
    act(() => openPanel('sub_ds'))
    const sec = await screen.findByRole('region', { name: 'Sessions liées · 6' })
    expect(sec.closest('.lpanel')).toBe(sheet())
    expect(ids(sec)).toEqual(['s-wait-bare', 's-wait-perm', 's-idle-q-mac', 's-busy-q', 's-idle-review', 's-paused'])
    for (const li of sec.querySelectorAll('li.srow')) expect(li).toHaveClass('compact')
    // an ended session of the subject never shows
    expect(PEERS_ALL.some((p) => p.ended_at)).toBe(true)
  })

  it('the accent only on a line that waits', async () => {
    await renderApp(withSessions())
    act(() => openPanel('sub_ds'))
    const sec = await screen.findByRole('region', { name: /^Sessions liées/ })
    const marked = [...sec.querySelectorAll<HTMLElement>('li.srow.sess-wait')].map((e) => e.dataset.sid)
    expect(marked).toEqual(['s-wait-bare', 's-wait-perm', 's-idle-q-mac'])
    expect(sec.querySelectorAll('li.srow:not(.sess-wait) .chip-attn')).toHaveLength(0)
  })

  it('a click opens that session’s panel in the Sessions view (the subject sheet closes)', async () => {
    await renderApp(withSessions())
    act(() => openPanel('sub_watering'))
    const sec = await screen.findByRole('region', { name: /^Sessions liées/ })
    const tick = getUi().focusTick
    fireEvent.click(sec.querySelector<HTMLElement>('li.srow[data-sid="s-idle-arb"]')!)
    expect(getUi()).toMatchObject({ view: 'sessions', sessionSel: 's-idle-arb', sessionPanel: true, panel: false })
    expect(getUi().focusTick).toBe(tick + 1)
    const p = document.querySelector<HTMLElement>('.spanel')!
    expect(p.dataset.sid).toBe('s-idle-arb')
    expect(within(p).getByRole('region', { name: 'Attend ta réponse' })).toHaveTextContent('Arbitrage')
  })

  it('⏎ on a focused line opens it too', async () => {
    await renderApp(withSessions())
    act(() => openPanel('sub_watering'))
    const sec = await screen.findByRole('region', { name: /^Sessions liées/ })
    const li = sec.querySelector<HTMLElement>('li.srow[data-sid="s-shell-working"]')!
    expect(li).toHaveAttribute('tabindex', '0')
    li.focus()
    fireEvent.keyDown(li, { key: 'Enter' })
    expect(getUi()).toMatchObject({ view: 'sessions', sessionSel: 's-shell-working', sessionPanel: true, panel: false })
  })

  it('no linked session: no section at all (no empty sentence)', async () => {
    const h = await renderApp(withSessions())
    act(() => openPanel('pig-r1'))
    await screen.findByRole('heading', { level: 2, name: 'Atlas web' })
    // positive control: the all-peers read has landed before the absence is asserted
    await waitFor(() => expect(h.qc.getQueryState(PEERS_ALL_KEY)?.status).toBe('success'))
    expect(linked()).toBeNull()
    expect(sheet().textContent).not.toMatch(/Sessions liées|Aucune session/)
  })

  it('one /peers?all=1 read shared with the view: the same cache entry, the view shows at once without loading', async () => {
    const h = await renderApp(withSessions())
    expect(allReads(h.fetchFn)).toBe(0)
    act(() => openPanel('sub_ds'))
    await screen.findByRole('region', { name: /^Sessions liées/ })
    expect(allReads(h.fetchFn)).toBe(1)
    expect(h.qc.getQueryCache().findAll({ queryKey: PEERS_ALL_KEY, exact: true })).toHaveLength(1)
    fireEvent.keyDown(document.body, { key: 'Escape' })
    fireEvent.keyDown(document.body, { key: 's' })
    expect(screen.getByRole('region', { name: 'Sessions' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Chargement des sessions')).toBeNull()
    expect(h.qc.getQueryCache().findAll({ queryKey: PEERS_ALL_KEY, exact: true })).toHaveLength(1)
  })

  it('no all-peers read while no sheet is open and the view is not shown', async () => {
    const h = await renderApp(withSessions())
    expect(allReads(h.fetchFn)).toBe(0)
    expect(getUi().panel).toBe(false)
  })
})
