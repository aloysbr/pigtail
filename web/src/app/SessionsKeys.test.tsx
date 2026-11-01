import { act, fireEvent, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { getUi, setUi } from '@/state/ui'
import { withSessions } from '@/test/peers-fixture'
import { renderApp } from '@/test/render'
import { pushToast, resetToasts } from '@/ui/toasts'

/** Lets the write queue drain: a mutation's onMutate awaits cancelQueries before fetch, so a POST leaves a few ticks after the key. Without it « no write » is read before the write could happen. */
async function settle() {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
}

/** The web front on the Sessions view, with a backlog subject selected behind it (so a leaked l or h would have a target). */
async function onSessions() {
  const h = await renderApp(withSessions())
  // the same subject and routes as the backlog's key matrix (list/Keyboard.test.tsx), where l, h and ⌥↓ do write
  h.setRoute('POST /subjects/pig-r1/launch', () => ({ status: 202, body: { data: { launch_id: 'ln_1', subject_id: 'pig-r1', state: 'pending', cell: 'w1', tab_title: 'x' } } }))
  h.setRoute('POST /subjects/pig-r1', () => ({ status: 200, body: { data: h.data().subjects.find((x) => x.id === 'pig-r1') } }))
  h.setRoute('POST /subjects/ranks', (req) => ({ status: 200, body: { data: (req.body as { changes: unknown[] }).changes } }))
  setUi({ sel: 'pig-r1' })
  fireEvent.keyDown(document.body, { key: 's' })
  await screen.findByRole('region', { name: 'Sessions' })
  return h
}

const key = (k: string, extra: Partial<KeyboardEventInit> = {}) => fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...extra })

describe('Sessions view keys', () => {
  it('↓ / j select the first session then the next ones in screen order, ↑ / k go back; the row takes the focus', async () => {
    await onSessions()
    const order = [...document.querySelectorAll<HTMLElement>('li.srow')].map((e) => e.dataset.sid)
    key('ArrowDown')
    expect(getUi().sessionSel).toBe(order[0])
    key('j')
    expect(getUi().sessionSel).toBe(order[1])
    expect(document.activeElement).toBe(document.querySelector(`li.srow[data-sid="${order[1]}"]`))
    key('k')
    key('ArrowUp')
    expect(getUi().sessionSel).toBe(order[0])
  })

  it('⏎ opens the selected session’s panel, never the subject sheet', async () => {
    await onSessions()
    key('j')
    key('Enter')
    expect(getUi()).toMatchObject({ view: 'sessions', sessionPanel: true, panel: false })
  })

  it('l never launches a session from the view: no write at all', async () => {
    const h = await onSessions()
    key('j')
    key('l')
    await settle()
    expect(h.writes()).toEqual([])
    expect(getUi().view).toBe('sessions')
  })

  it('h never sets Standby: no write at all, once the write queue is drained', async () => {
    const h = await onSessions()
    key('j')
    key('h', { code: 'KeyH' })
    await settle()
    expect(h.writes()).toEqual([])
  })

  it('⌥↑ / ⌥↓ never move a rank: no write at all, once the write queue is drained', async () => {
    const h = await onSessions()
    key('j')
    key('ArrowDown', { altKey: true, code: 'ArrowDown' })
    key('ArrowUp', { altKey: true, code: 'ArrowUp' })
    await settle()
    expect(h.writes()).toEqual([])
    expect(getUi().sel).toBe('pig-r1')
  })

  it('z never undoes: an undoable action on screen stays undoable', async () => {
    await onSessions()
    const run = vi.fn(async () => {})
    resetToasts()
    act(() => {
      pushToast({ text: 'Rang modifié', undo: { title: 'X', label: 'X', run, stillValid: () => true } })
    })
    key('z')
    await settle()
    expect(run).not.toHaveBeenCalled()
  })

  it('the other backlog keys are inert there: f, v, g, ?, «,», c, ← → change nothing and write nothing', async () => {
    const h = await onSessions()
    key('j')
    const { focusTick: _t, ...before } = getUi()
    for (const k of ['f', 'v', 'g', '?', ',', 'c', 'ArrowLeft', 'ArrowRight']) key(k)
    await settle()
    const { focusTick: _t2, ...after } = getUi()
    expect(after).toEqual(before)
    expect(h.writes()).toEqual([])
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('/ focuses the sessions filter, not the backlog search', async () => {
    await onSessions()
    key('/')
    expect(document.activeElement).toHaveAttribute('id', 'sessions-search')
  })

  it('typing in the filter is not a key: j there types', async () => {
    const h = await onSessions()
    key('/')
    await h.user.keyboard('j')
    expect(getUi().sessQuery).toBe('j')
    expect(getUi().sessionSel).toBeNull()
  })

  it('a deep link to a session whose scope pill is off opens its panel, without the previous / next buttons (it has no place in the shown order)', async () => {
    await onSessions()
    act(() => setUi({ scopesOff: ['globex'], sessionSel: 's-wk-busy', sessionPanel: true }))
    expect(document.querySelector('.pwrap > .spanel')).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'Session précédente' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Session suivante' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Fermer le panneau' })).toBeInTheDocument()
  })
})
