import { act, fireEvent, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { groupButton, renderApp } from '@/test/render'
import { openPanel, setUi } from '@/state/ui'
import { resetWidths } from '@/state/widths'

describe('Layout structure (verify round, lot 2)', () => {
  it('C8: the shortcut footer sits outside the scrolling list, so it is opaque by construction and glued to the bottom', async () => {
    await renderApp()
    const scroller = document.querySelector('[data-scroll="list"]') as HTMLElement
    expect(scroller).not.toBeNull()
    expect(scroller.contains(screen.getByRole('contentinfo', { name: 'Raccourcis clavier' }))).toBe(false)
    expect(scroller.contains(document.querySelector('header.d-head'))).toBe(true)
    expect(scroller.contains(document.querySelector('.ll6'))).toBe(true)
  })

  it('C8: the hidden-footer hint is outside the scroller too', async () => {
    await renderApp()
    act(() => setUi({ kbHidden: true }))
    const scroller = document.querySelector('[data-scroll="list"]') as HTMLElement
    expect(scroller.contains(screen.getByRole('button', { name: /raccourcis/ }))).toBe(false)
  })

  it('C9: under 640 px the shell carries the phone class (keys are hidden by CSS); wider it does not', async () => {
    const a = await renderApp(undefined, { width: 390 })
    expect(document.querySelector('.app')).toHaveClass('phone')
    a.unmount()
    await renderApp(undefined, { width: 1024 })
    expect(document.querySelector('.app')).not.toHaveClass('phone')
  })

  it('B2: a status group marks its section so its rows indent under the group icon', async () => {
    await renderApp()
    act(() => setUi({ group: 'statut' }))
    const sec = groupButton('st:ouvert').closest('section')
    expect(sec).toHaveClass('by-status')
  })

  it('C1: the project chip text is its own ellipsis-able element, with the full name as tooltip', async () => {
    await renderApp()
    act(() => setUi({ group: 'statut', open: { 'st:ouvert': true } }))
    const t = document.querySelector('.lr-th .chip .chip-t')
    expect(t).not.toBeNull()
    expect(t?.closest('.chip')).toHaveAttribute('title', t?.textContent ?? '')
  })

  it('toolbar: count, Abandonnés and Grouper form one cluster that wraps as a unit', async () => {
    await renderApp()
    const tools = document.querySelector('.d-head .d-tools') as HTMLElement
    expect(tools).not.toBeNull()
    expect(tools.querySelector('.fcount')).not.toBeNull()
    expect(tools).toHaveTextContent(/Abandonnés/)
    expect(tools).toHaveTextContent(/Grouper/)
  })

  it('C5: every area line of the Filtre menu has a count cell and a key cell (empty past 9), so columns align', async () => {
    const { user } = await renderApp()
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    const items = [...document.querySelectorAll('.gmenu [role="menuitemradio"]')].filter((b) => b.querySelector('.gm-n'))
    expect(items.length).toBeGreaterThan(0)
    for (const b of items) expect(b.querySelector('.gm-k')).not.toBeNull()
  })
})

const app = () => document.querySelector('.app') as HTMLElement
const head = () => document.querySelector('header.d-head') as HTMLElement

describe('Sheet modes by width (t10 pmode, lot 3)', () => {
  it('1440: docked sidebar, the sheet beside the list', async () => {
    await renderApp(undefined, { width: 1440 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-side')
    expect(app()).not.toHaveClass('nodock')
    expect(screen.getByRole('button', { name: 'Fermer le panneau' })).toBeInTheDocument()
  })

  it('1600: the wide breakpoint gives the sheet its wide width', async () => {
    await renderApp(undefined, { width: 1600 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-side', 'bp-wide')
  })

  it('1024: floating sidebar, the sheet still beside the list (464 px of list left)', async () => {
    await renderApp(undefined, { width: 1024 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-side', 'nodock')
  })

  it('900: under 400 px of list the sheet goes over it', async () => {
    await renderApp(undefined, { width: 900 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-over')
    expect(app()).not.toHaveClass('pm-side')
  })

  it('390: the sheet is the page, with « Retour à la liste »; the « Retour à la liste » button closes the sheet', async () => {
    await renderApp(undefined, { width: 390 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-page', 'phone')
    const back = screen.getByRole('button', { name: 'Retour à la liste' })
    expect(screen.queryByRole('button', { name: 'Fermer le panneau' })).toBeNull()
    fireEvent.click(back)
    expect(app()).not.toHaveClass('pm-page')
    expect(document.querySelector('.lpanel')).toBeNull()
  })

  it('390: page mode keeps Retour and adds ‹ ›', async () => {
    await renderApp(undefined, { width: 390 })
    act(() => openPanel('pig-r1'))
    const bar = document.querySelector('.lp-h') as HTMLElement
    expect(bar.querySelector('button')).toBe(screen.getByRole('button', { name: 'Retour à la liste' }))
    expect(screen.getByRole('button', { name: 'Sujet précédent' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sujet suivant' })).toBeInTheDocument()
  })


  it('390: Esc on the page goes back to the list', async () => {
    await renderApp(undefined, { width: 390 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-page')
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
    expect(app()).not.toHaveClass('pm-page')
    expect(document.querySelector('.lpanel')).toBeNull()
  })

  it('no sheet, no mode class', async () => {
    await renderApp(undefined, { width: 1440 })
    expect(app().className).not.toMatch(/pm-/)
  })

  it('the header folds by the list width that the sheet leaves beside it', async () => {
    await renderApp(undefined, { width: 1440 })
    expect(head()).toHaveClass('hd-0')
    act(() => openPanel('pig-r1'))
    // 1440 - 240 - 560 = 640 px of list: icons
    expect(head()).toHaveClass('hd-2')
  })
})

/** Puts a stored width in place before the render, as a page load would read it. */
function storeWidth(key: 'pigtail-side-w' | 'pigtail-panel-w', px: number) {
  localStorage.setItem(key, String(px))
  resetWidths()
}

describe('Stored widths drive the sheet mode and the grid (H1.1, D2)', () => {
  it('a 360 px sidebar at 1200 puts the sheet over the list', async () => {
    // default: 1200 - 240 - 560 = 400 px of list, the sheet stays beside it
    const a = await renderApp(undefined, { width: 1200 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-side')
    a.unmount()
    // 1200 - 360 - 560 = 280 < 400: over the list
    storeWidth('pigtail-side-w', 360)
    await renderApp(undefined, { width: 1200 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-over')
    expect(app().style.getPropertyValue('--size-sidebar')).toBe('360px')
  })

  it('a stored sheet width sizes the grid', async () => {
    storeWidth('pigtail-panel-w', 480)
    await renderApp(undefined, { width: 1440 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-side')
    expect(app().style.getPropertyValue('--size-panel')).toBe('480px')
    expect(app().style.getPropertyValue('--size-panel-xl')).toBe('480px')
  })

  it('a stored sheet outside the range renders inside it', async () => {
    // 1120 docked: 880 px left, the sheet over the list may reach its 560 px default, not 800
    storeWidth('pigtail-panel-w', 800)
    await renderApp(undefined, { width: 1120 })
    act(() => openPanel('pig-r1'))
    expect(app()).toHaveClass('pm-over')
    expect(app().style.getPropertyValue('--size-panel')).toBe('560px')
    // the separator says the width it renders, inside its own announced range
    const sep = document.querySelector('[role="separator"][aria-controls="sheet"]') as HTMLElement
    expect(sep).not.toBeNull()
    const v = (a: string) => Number(sep.getAttribute(a))
    expect(v('aria-valuenow')).toBe(560)
    expect(v('aria-valuenow')).toBeGreaterThanOrEqual(v('aria-valuemin'))
    expect(v('aria-valuenow')).toBeLessThanOrEqual(v('aria-valuemax'))
  })
})
