import { act, fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { getUi, openPanel, setUi } from '@/state/ui'
import { withSessions } from '@/test/peers-fixture'
import { renderApp } from '@/test/render'

async function onSessions(width?: number) {
  const h = await renderApp(withSessions(), width ? { width } : {})
  fireEvent.keyDown(document.body, { key: 's' })
  await screen.findByRole('region', { name: 'Sessions' })
  return h
}

const row = (sid: string) => document.querySelector<HTMLElement>(`li.srow[data-sid="${sid}"]`)!
const panel = () => document.querySelector<HTMLElement>('.spanel')
const key = (k: string, extra: Partial<KeyboardEventInit> = {}) => fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...extra })

describe('Session panel', () => {
  it('a click on a row opens it: serif short title, « Session <cellule> · <projet> », the wait card of the gravest wait', async () => {
    await onSessions()
    fireEvent.click(row('s-idle-q-mac'))
    const p = panel()!
    expect(p).not.toBeNull()
    expect(within(p).getByRole('heading', { level: 2 })).toHaveClass('lp-t')
    expect(within(p).getByRole('heading', { level: 2 })).toHaveTextContent('Atlas DS')
    expect(p.querySelector('.lp-sub')).toHaveTextContent('Session w3 · pigtail')
    const card = within(p).getByRole('region', { name: 'Attend ta réponse' })
    // waits[0] is the held mac: the card shows the gravest, the declared question
    expect(card.querySelector('.kind')).toHaveTextContent('Question')
    expect(card).toHaveTextContent('Quel compte ?')
    expect(card.querySelector('.wait-h')?.textContent).toMatch(/depuis/)
  })

  it('⏎ on the selected row opens it too; the trail names its group', async () => {
    await onSessions()
    key('j')
    key('Enter')
    expect(panel()).not.toBeNull()
    expect(panel()!.querySelector('.lp-trail')).toHaveTextContent('Sessions › Attend ta réponse')
  })

  it('the properties are only those /peers carries: Cellule, Worktree, Statut (life written out)', async () => {
    await onSessions()
    fireEvent.click(row('s-idle-arb'))
    const dts = [...panel()!.querySelectorAll('.lp-props dt')].map((d) => d.textContent)
    expect(dts).toEqual(['Cellule', 'Worktree', 'Statut'])
    const dd = (label: string) => [...panel()!.querySelectorAll('.lp-props dt')].find((d) => d.textContent === label)!.nextElementSibling!
    expect(dd('Cellule')).toHaveTextContent('w3')
    expect(dd('Worktree')).toHaveTextContent('watering')
    expect(dd('Statut')).toHaveTextContent('Attend ta réponse · Vivante')
  })

  it('a property with no data is absent: no worktree, no Worktree row; a paused session says since when', async () => {
    await onSessions()
    fireEvent.click(row('s-shell-bare'))
    expect([...panel()!.querySelectorAll('.lp-props dt')].map((d) => d.textContent)).toEqual(['Cellule', 'Statut'])
    fireEvent.click(row('s-paused'))
    const dd = [...panel()!.querySelectorAll('.lp-props dt')].find((d) => d.textContent === 'Statut')!.nextElementSibling!
    expect(dd.textContent).toMatch(/^Inactive · En pause depuis \d\d:\d\d$/)
    expect([...panel()!.querySelectorAll('.lp-props dt')].map((d) => d.textContent)).toContain('Worktree')
  })

  it('a session that works or rests: the same panel, no wait card', async () => {
    await onSessions()
    fireEvent.click(row('s-busy-q'))
    expect(panel()!.querySelector('.wait')).toBeNull()
    fireEvent.click(row('s-idle-review'))
    expect(panel()!.querySelector('.wait')).toBeNull()
  })

  it('absent: model, context, account, PR, servers, last activity, Open in terminal, Respawn, Move, Stop, any answer button', async () => {
    await onSessions()
    fireEvent.click(row('s-wait-perm'))
    const p = panel()!
    expect(p.textContent).not.toMatch(/Modèle|Contexte|compte|PR\b|Serveurs|Dernière activité|Open in terminal|Respawn|Move|Stop|Répondre|Go ahead|Étape/)
    const names = within(p).queryAllByRole('button').map((b) => b.getAttribute('aria-label') ?? b.textContent)
    expect(names).toEqual(['Fermer le panneau', 'Session précédente', 'Session suivante'])
  })

  it('previous / next carry up / down icons (the keys are ↑ ↓, not ← →)', async () => {
    await onSessions()
    fireEvent.click(row('s-idle-q-mac'))
    const d = (name: string) => within(panel()!).getByRole('button', { name }).querySelector('path')!.getAttribute('d')
    expect(d('Session précédente')).toBe('M4 10l4-4 4 4')
    expect(d('Session suivante')).toBe('M4 6l4 4 4-4')
  })

  it('« Ouvrir le sujet » goes to #/s/<id>: the subject sheet in the backlog, the session panel closed', async () => {
    await onSessions()
    fireEvent.click(row('s-idle-arb'))
    const link = within(panel()!).getByRole('link', { name: 'Ouvrir le sujet' })
    expect(link).toHaveAttribute('href', '#/s/sub_watering')
    fireEvent.click(link)
    expect(getUi()).toMatchObject({ view: 'backlog', sel: 'sub_watering', panel: true, sessionPanel: false })
    expect(panel()).toBeNull()
    expect(document.querySelector('.lpanel #lp-title')).toHaveTextContent('Watering')
  })

  it('a session with no subject known to the caches has no « Ouvrir le sujet »', async () => {
    await onSessions()
    fireEvent.click(row('s-idle-stale'))
    expect(within(panel()!).queryByRole('link', { name: 'Ouvrir le sujet' })).toBeNull()
    fireEvent.click(row('s-wk-busy'))
    expect(within(panel()!).queryByRole('link', { name: 'Ouvrir le sujet' })).toBeNull()
  })

  it('↓ / ↑ step to the next / previous session, the panel follows; the step buttons do the same', async () => {
    await onSessions()
    const order = [...document.querySelectorAll<HTMLElement>('li.srow')].map((e) => e.dataset.sid)
    fireEvent.click(row(order[1]!))
    key('ArrowDown')
    expect(getUi().sessionSel).toBe(order[2])
    expect(panel()!.dataset.sid).toBe(order[2])
    key('k')
    expect(panel()!.dataset.sid).toBe(order[1])
    fireEvent.click(screen.getByRole('button', { name: 'Session suivante' }))
    expect(panel()!.dataset.sid).toBe(order[2])
    fireEvent.click(screen.getByRole('button', { name: 'Session précédente' }))
    expect(panel()!.dataset.sid).toBe(order[1])
  })

  it('Esc closes it and gives the focus back to its row', async () => {
    await onSessions()
    fireEvent.click(row('s-busy-q'))
    key('Escape')
    expect(panel()).toBeNull()
    expect(getUi().sessionPanel).toBe(false)
    expect(document.activeElement).toBe(row('s-busy-q'))
  })

  it('one panel at a time: a subject sheet closes the session panel, a session closes the subject sheet', async () => {
    await onSessions()
    fireEvent.click(row('s-busy-q'))
    act(() => openPanel('pig-r1'))
    expect(panel()).toBeNull()
    expect(document.querySelectorAll('.pwrap')).toHaveLength(1)
    act(() => setUi({ view: 'sessions' }))
    fireEvent.click(await screen.findByText((_, el) => el?.getAttribute('data-sid') === 's-busy-q'))
    expect(getUi().panel).toBe(false)
    expect(document.querySelectorAll('.pwrap')).toHaveLength(1)
    expect(panel()).not.toBeNull()
  })

  it('widths: beside the list on a wide window, the whole page on a phone, like the sheet', async () => {
    await onSessions(1440)
    fireEvent.click(row('s-busy-q'))
    expect(document.querySelector('.app')).toHaveClass('pm-side')
    expect(panel()!.closest('.pwrap')).not.toBeNull()
  })

  it('page mode (phone): a « Sessions » back button instead of the ×', async () => {
    await onSessions(390)
    fireEvent.click(row('s-busy-q'))
    expect(document.querySelector('.app')).toHaveClass('pm-page')
    const back = within(panel()!).getByRole('button', { name: 'Retour aux sessions' })
    // the « < Sessions » back button already says it: no « Sessions › groupe » trail beside it
    expect(panel()!.querySelector('.lp-trail')).toBeNull()
    fireEvent.click(back)
    expect(panel()).toBeNull()
  })

  it('a route naming a session that /peers?all=1 does not hold closes the panel (back to #/sessions)', async () => {
    await onSessions()
    act(() => setUi({ sessionSel: 's-nobody', sessionPanel: true }))
    await screen.findByRole('region', { name: 'Sessions' })
    expect(getUi().sessionPanel).toBe(false)
    expect(panel()).toBeNull()
  })
})
