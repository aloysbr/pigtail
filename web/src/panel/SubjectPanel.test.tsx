import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { openPanel } from '@/state/ui'
import { makeBacklog } from '@/test/backlog'
import { groupButton, renderApp, rowIds } from '@/test/render'

function panel(): HTMLElement {
  const p = document.querySelector<HTMLElement>('.lpanel')
  if (!p) throw new Error('no subject panel')
  return p
}

/** The dd that follows a dt of the panel's property list. */
function prop(label: string): HTMLElement {
  const dt = [...panel().querySelectorAll('dt')].find((x) => x.textContent === label)
  const dd = dt?.nextElementSibling
  if (!(dd instanceof HTMLElement)) throw new Error(`no property ${label}`)
  return dd
}

async function openRow(id: string) {
  const r = await renderApp()
  await r.user.click(groupButton('th:th_pig'))
  await r.user.click(document.querySelector(`li[data-id="${id}"]`) as HTMLElement)
  return r
}

describe('SubjectPanel', () => {
  it('a click on a row opens its sheet beside the list; the list stays', async () => {
    await openRow('pig-r1')
    expect(screen.getByRole('region', { name: 'Atlas web' })).toBe(panel())
    expect(screen.getByRole('region', { name: 'Sujets' })).toBeInTheDocument()
  })

  it('shows the trail « scope › aire › projet », the status icon and short title, then the long title', async () => {
    await openRow('pig-r1')
    expect(panel().querySelector('.lp-h')).toHaveTextContent('perso › Allotment › pigtail')
    const ttl = panel().querySelector('.lp-ttl') as HTMLElement
    expect(within(ttl).getByRole('img', { name: 'Ouvert' })).toBeInTheDocument()
    expect(within(ttl).getByRole('heading', { level: 2 })).toHaveTextContent('Atlas web')
    expect(panel().querySelector('.lp-sub')).toHaveTextContent('Backlog en lecture')
  })

  it('Statut: the displayed label with the stored value as tooltip, then the phase', async () => {
    await openRow('pig-r1')
    const st = prop('Statut')
    expect(within(st).getByText('Ouvert')).toHaveAttribute('title', 'Statut stocké : building')
    expect(st).toHaveTextContent('Construire')
  })

  it('a subject with no phase says « pas commencé »; an unranked one « non classé »', async () => {
    await openRow('pig-i1')
    expect(prop('Statut')).toHaveTextContent('pas commencé')
    expect(prop('Rang')).toHaveTextContent('non classé')
  })

  it('« Aire › projet » and « Rang » of a ranked subject', async () => {
    await openRow('pig-r1')
    expect(prop('Aire › projet')).toHaveTextContent('Allotment › pigtail')
    expect(prop('Aire › projet')).toHaveTextContent('perso')
    expect(prop('Rang')).toHaveTextContent('#1')
  })

  it('a subject with no theme: « Sans aire › Sans projet » in the trail and the field', async () => {
    const r = await renderApp()
    await r.user.click(groupButton('th:none:perso'))
    await r.user.click(document.querySelector('li[data-id="orphan"]') as HTMLElement)
    expect(panel().querySelector('.lp-h')).toHaveTextContent('perso › Sans aire › Sans projet')
    expect(prop('Aire › projet')).toHaveTextContent('Sans aire › Sans projet')
  })

  it('the sections come in the spec order: title, Statut, Aire › projet, Rang, Liens, Historique', async () => {
    await openRow('pig-r1')
    const p = panel()
    const order = [...p.querySelectorAll('h2, dt, h3')].map((x) => x.textContent)
    expect(order).toEqual(['Atlas web', 'Statut', 'Aire › projet', 'Rang', 'Cycle de vie', 'Liens', 'Historique'])
  })

  it('× closes the sheet', async () => {
    const { user } = await openRow('pig-r1')
    await user.click(screen.getByRole('button', { name: 'Fermer le panneau' }))
    expect(document.querySelector('.lpanel')).toBeNull()
  })

  it('× opens the head and shows no shortcut', async () => {
    await openRow('pig-r1')
    const head = panel().querySelector('.lp-h') as HTMLElement
    const first = head.querySelector('button, a') as HTMLElement
    expect(first).toBe(screen.getByRole('button', { name: 'Fermer le panneau' }))
    expect(first).toHaveAttribute('aria-keyshortcuts', 'Escape')
    expect(head.querySelector('kbd')).toBeNull()
  })

  it('‹ › step through the subjects', async () => {
    const { user } = await openRow('pig-r1')
    const ids = rowIds()
    const at = ids.indexOf('pig-r1')
    expect(at).toBeGreaterThanOrEqual(0)
    expect(ids.length).toBeGreaterThan(at + 1)
    const sel = () => document.querySelector('li.lr6.sel')?.getAttribute('data-id')
    await user.click(screen.getByRole('button', { name: 'Sujet suivant' }))
    expect(sel()).toBe(ids[at + 1])
    await user.click(screen.getByRole('button', { name: 'Sujet précédent' }))
    expect(sel()).toBe('pig-r1')
    // the sheet follows the selection and the focus stays on the button
    expect(screen.getByRole('button', { name: 'Sujet précédent' })).toHaveFocus()
  })

  it('‹ › are aria-disabled (never disabled) at the ends, keep the focus and do nothing', async () => {
    // Open the ends directly: walking the whole list click by click took
    // most of the 5 s budget and timed out on a loaded machine.
    const { user } = await openRow('pig-r1')
    const ids = rowIds()
    const row = (id: string) => document.querySelector(`li[data-id="${id}"]`) as HTMLElement
    const sel = () => document.querySelector('li.lr6.sel')?.getAttribute('data-id')
    const prev = () => screen.getByRole('button', { name: 'Sujet précédent' })
    const next = () => screen.getByRole('button', { name: 'Sujet suivant' })
    await user.click(row(ids[ids.length - 1]))
    expect(next()).toHaveAttribute('aria-disabled', 'true')
    expect(next()).not.toBeDisabled()
    await user.click(next())
    expect(sel()).toBe(ids[ids.length - 1])
    expect(next()).toHaveFocus()
    await user.click(row(ids[0]))
    expect(prev()).toHaveAttribute('aria-disabled', 'true')
    expect(prev()).not.toBeDisabled()
    await user.click(prev())
    expect(sel()).toBe(ids[0])
    expect(prev()).toHaveFocus()
    // Up to 100 user clicks walk the list end to end: past vitest's 5 s
    // default when the full `make check` runs every jsdom file in parallel.
  }, 20_000)

  it('a Done subject’s actions area is empty (Lancer is offered on Ouvert and Standby only)', async () => {
    await openRow('pig-r1')
    act(() => openPanel('pig-d1'))
    const acts = panel().querySelector('[data-slot="actions"]')
    expect(acts).not.toBeNull()
    expect(acts?.childElementCount).toBe(0)
  })

  it('a captured subject revealed by « Abandonnés » says « Capturé » (G10)', async () => {
    const data = makeBacklog()
    const r = await renderApp(data)
    await r.user.click(screen.getByRole('button', { name: /Abandonnés/ }))
    await r.user.click(groupButton('th:th_pig'))
    await r.user.click(groupButton('th:th_pig:dropped'))
    await r.user.click(document.querySelector('li[data-id="pig-c1"]') as HTMLElement)
    expect(within(prop('Statut')).getByText('Capturé')).toHaveAttribute('title', 'Statut stocké : captured')
  })

  it('« Cycle de vie »: the Capture chip has its icon, the third chip is the cy-mid one (verify V2 d)', async () => {
    await openRow('pig-r1')
    const box = within(panel()).getByRole('group', { name: /Position/ })
    const chips = [...box.querySelectorAll('.cy')]
    expect(chips.map((c) => c.querySelector('.cy-i + span')?.textContent)).toEqual(['Capture', 'Ouvert', 'En cours', 'Done'])
    for (const c of chips) expect(c.querySelector('.cy-i svg')).not.toBeNull()
    expect(chips[2]).toHaveClass('cy-mid')
  })

  it('an Abandonné sheet carries t10\'s muted line; a captured one does not (verify V2 e)', async () => {
    const r = await renderApp(makeBacklog())
    await r.user.click(screen.getByRole('button', { name: /Abandonnés/ }))
    await r.user.click(groupButton('th:th_pig'))
    await r.user.click(groupButton('th:th_pig:dropped'))
    await r.user.click(document.querySelector('li[data-id="pig-x1"]') as HTMLElement)
    const line = within(panel()).getByText('Abandonné : sorti du cycle, visible derrière « Voir les abandonnés ».')
    expect(line).toHaveClass('muted')
    await r.user.click(document.querySelector('li[data-id="pig-c1"]') as HTMLElement)
    expect(within(panel()).queryByText(/sorti du cycle/)).toBeNull()
  })

  it('a Standby says « Standby » alone in « Statut », no invented phase (verify i)', async () => {
    await renderApp()
    act(() => openPanel('pig-p1'))
    expect(prop('Statut')).toHaveTextContent('Standby')
    expect(prop('Statut')).not.toHaveTextContent('pas commencé')
  })

  it('panel.css: icons sized in their box, phone chips at --radius-chip-m with their arrows, switch hit area without growing the chip (verify V2 a-c)', () => {
    const css = readFileSync(join(process.cwd(), 'src/panel/panel.css'), 'utf8')
    expect(css).toMatch(/\.cy-i \.si \{[^}]*width: var\(--size-icon-sm\)/)
    expect(css).toMatch(/\.phone \.cy \{[^}]*border-radius: var\(--radius-chip-m\)/)
    expect(css).not.toMatch(/\.phone \.cy-a \{[^}]*display: none/)
    expect(css).toMatch(/\.phone \.cy-sw::before \{[^}]*var\(--size-touch\)/)
    expect(css).not.toMatch(/\.phone \.cy-sw \{[^}]*min-height: var\(--size-touch\)/)
  })

  it('panel.css: at 390 px the lifecycle row fits 358 px (verify round 2): narrower arrow columns, tighter chips and switch on the phone', () => {
    const css = readFileSync(join(process.cwd(), 'src/panel/panel.css'), 'utf8')
    expect(css).toMatch(/\.phone \.cycle \{[^}]*grid-template-columns: auto var\(--size-icon-sm\) auto var\(--size-icon-sm\) auto var\(--size-icon-sm\) auto/)
    expect(css).toMatch(/\.phone \.cy \{[^}]*gap: var\(--sp-1\)/)
    expect(css).toMatch(/\.phone \.cy-sw \{[^}]*margin-left: var\(--sp-1\)/)
  })

  it('« Lancer l » sits in the sheet’s actions on an Ouvert; none on En cours or Done', async () => {
    await openRow('pig-r1')
    expect(panel().querySelector('.lp-acts button.launch-btn')).toHaveTextContent('Lancer')
    act(() => openPanel('pig-live'))
    expect(panel().querySelector('button.launch-btn')).toBeNull()
    act(() => openPanel('pig-d1'))
    expect(panel().querySelector('button.launch-btn')).toBeNull()
  })
})
