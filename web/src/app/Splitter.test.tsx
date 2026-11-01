import { act, fireEvent, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { openPanel, setUi } from '@/state/ui'
import { getWidths, resetWidths } from '@/state/widths'
import { renderApp, unfoldAll } from '@/test/render'

const SIDE = 'Largeur de la barre latérale'
const SHEET = 'Largeur de la fiche'
const sep = (name: string) => screen.getByRole('separator', { name })
const now = (el: HTMLElement) => Number(el.getAttribute('aria-valuenow'))
const min = (el: HTMLElement) => Number(el.getAttribute('aria-valuemin'))
const max = (el: HTMLElement) => Number(el.getAttribute('aria-valuemax'))
const key = (el: HTMLElement, k: string, shiftKey = false) => fireEvent.keyDown(el, { key: k, shiftKey })

/** A whole drag: press on the separator, one move to clientX, release. */
function drag(el: HTMLElement, x: number) {
  fireEvent.pointerDown(el, { pointerId: 1, clientX: x, button: 0 })
  fireEvent.pointerMove(el, { pointerId: 1, clientX: x })
  fireEvent.pointerUp(el, { pointerId: 1, clientX: x })
}

describe('Separators (D2, D5): sidebar and sheet widths', () => {
  it('named separators', async () => {
    await renderApp(undefined, { width: 1440 })
    act(() => openPanel('pig-r1'))
    const s = sep(SIDE)
    expect(s).toHaveAttribute('aria-orientation', 'vertical')
    expect(s).toHaveAttribute('tabindex', '0')
    expect(now(s)).toBe(240)
    expect(min(s)).toBe(200)
    expect(max(s)).toBe(360)
    expect(s).toHaveAttribute('aria-valuetext', 'Barre latérale 240 px')
    const p = sep(SHEET)
    expect(p).toHaveAttribute('aria-orientation', 'vertical')
    expect(p).toHaveAttribute('aria-controls', 'sheet')
    expect(document.getElementById('sheet')).not.toBeNull()
    // 1440 - 240 = 1200 left: the sheet 400-800, the list 1200 - 560
    expect(now(p)).toBe(560)
    expect(min(p)).toBe(400)
    expect(max(p)).toBe(800)
    expect(p).toHaveAttribute('aria-valuetext', 'Fiche 560 px, liste 640 px')
    const tip = s.querySelector('span.split-tip')
    expect(tip).toHaveAttribute('aria-hidden', 'true')
    // « double‑clic » never breaks at its hyphen (U+2011, non-breaking)
    expect(tip?.textContent).toBe('←→largeur · double‑clic : par\u00a0défaut')
    expect(tip?.textContent).not.toMatch(/-/)
    // « par défaut » never breaks between its words (U+00A0, no-break space)
    expect(tip?.textContent).toContain('par\u00a0défaut')
  })

  it('arrows step 16, shift 64, Home/End bounds', async () => {
    await renderApp(undefined, { width: 1440 })
    const s = sep(SIDE)
    key(s, 'ArrowRight')
    expect(now(s)).toBe(256)
    key(s, 'ArrowRight', true)
    expect(now(s)).toBe(320)
    key(s, 'ArrowLeft')
    expect(now(s)).toBe(304)
    key(s, 'Home')
    expect(now(s)).toBe(200)
    key(s, 'ArrowLeft')
    expect(now(s)).toBe(200)
    key(s, 'End')
    expect(now(s)).toBe(360)
    expect(localStorage.getItem('pigtail-side-w')).toBe('360')
    expect((document.querySelector('.app') as HTMLElement).style.getPropertyValue('--size-sidebar')).toBe('360px')
  })

  it('the sheet separator at 1120 stays in range', async () => {
    await renderApp(undefined, { width: 1120 })
    act(() => openPanel('pig-r1'))
    expect(document.querySelector('.app')).toHaveClass('pm-over')
    const p = sep(SHEET)
    expect(min(p)).toBeLessThanOrEqual(now(p))
    expect(now(p)).toBeLessThanOrEqual(max(p))
    key(p, 'End')
    expect(now(p)).toBe(max(p))
    expect(max(p)).toBeGreaterThanOrEqual(560)
  })

  it('a stored sheet outside the range: aria-valuenow is inside it', async () => {
    localStorage.setItem('pigtail-panel-w', '800')
    resetWidths()
    await renderApp(undefined, { width: 1120 })
    act(() => openPanel('pig-r1'))
    const p = sep(SHEET)
    expect(now(p)).toBe(560)
    expect(max(p)).toBe(560)
  })

  it('← grows the sheet', async () => {
    await renderApp(undefined, { width: 1440 })
    act(() => openPanel('pig-r1'))
    const p = sep(SHEET)
    key(p, 'ArrowLeft')
    expect(now(p)).toBe(576)
    key(p, 'ArrowRight', true)
    expect(now(p)).toBe(512)
    expect((document.querySelector('.app') as HTMLElement).style.getPropertyValue('--size-panel')).toBe('512px')
  })

  it('dragging the sheet separator sets and keeps its width', async () => {
    await renderApp(undefined, { width: 1440 })
    act(() => openPanel('pig-r1'))
    const p = sep(SHEET)
    drag(p, 900)
    expect(now(p)).toBe(1440 - 900)
    expect(localStorage.getItem('pigtail-panel-w')).toBe('540')
    act(() => resetWidths())
    expect(getWidths().panel).toBe(540)
    expect(now(sep(SHEET))).toBe(540)
    // past the bound: held at the max
    drag(sep(SHEET), 100)
    expect(now(sep(SHEET))).toBe(800)
  })

  it('drag sets the width', async () => {
    await renderApp(undefined, { width: 1440 })
    const s = sep(SIDE)
    fireEvent.pointerDown(s, { pointerId: 1, clientX: 240, button: 0 })
    expect(document.querySelector('.app')).toHaveClass('resizing')
    expect(s).toHaveClass('drag')
    fireEvent.pointerMove(s, { pointerId: 1, clientX: 300 })
    // nothing is kept before the release
    expect(localStorage.getItem('pigtail-side-w')).toBeNull()
    fireEvent.pointerUp(s, { pointerId: 1, clientX: 300 })
    expect(now(s)).toBe(300)
    expect(localStorage.getItem('pigtail-side-w')).toBe('300')
    expect(document.querySelector('.app')).not.toHaveClass('resizing')
    expect(s).not.toHaveClass('drag')
    // under the minimum: stops at 200
    drag(s, 120)
    expect(now(s)).toBe(200)
    expect(localStorage.getItem('pigtail-side-w')).toBe('200')
  })

  it('double-click resets', async () => {
    await renderApp(undefined, { width: 1440 })
    act(() => openPanel('pig-r1'))
    const s = sep(SIDE)
    key(s, 'End')
    expect(localStorage.getItem('pigtail-side-w')).toBe('360')
    fireEvent.doubleClick(s)
    expect(now(s)).toBe(240)
    expect(localStorage.getItem('pigtail-side-w')).toBeNull()
    const p = sep(SHEET)
    key(p, 'ArrowLeft')
    expect(localStorage.getItem('pigtail-panel-w')).toBe('576')
    fireEvent.doubleClick(p)
    expect(now(p)).toBe(560)
    expect(localStorage.getItem('pigtail-panel-w')).toBeNull()
  })

  it('arrows on a separator never fold a group', async () => {
    await renderApp(undefined, { width: 1440 })
    unfoldAll()
    const row = document.querySelector<HTMLElement>('li.lr6[data-id]') as HTMLElement
    expect(row).not.toBeNull()
    const id = row.dataset.id as string
    act(() => setUi({ sel: id }))
    const open = () => [...document.querySelectorAll('button[data-g][aria-expanded="true"]')].length
    const before = open()
    expect(before).toBeGreaterThan(0)
    const s = sep(SIDE)
    s.focus()
    key(s, 'ArrowLeft')
    expect(open()).toBe(before)
    key(s, 'ArrowRight')
    expect(open()).toBe(before)
    expect(document.querySelector(`li.lr6[data-id="${CSS.escape(id)}"]`)).not.toBeNull()
  })

  it('no sheet separator in page mode, no sidebar separator undocked', async () => {
    const a = await renderApp(undefined, { width: 390 })
    act(() => openPanel('pig-r1'))
    expect(document.querySelector('.app')).toHaveClass('pm-page')
    expect(screen.queryByRole('separator', { name: SHEET })).toBeNull()
    expect(screen.queryByRole('separator', { name: SIDE })).toBeNull()
    a.unmount()
    await renderApp(undefined, { width: 900 })
    expect(document.querySelector('.app')).toHaveClass('nodock')
    expect(screen.queryByRole('separator', { name: SIDE })).toBeNull()
    act(() => openPanel('pig-r1'))
    expect(screen.getByRole('separator', { name: SHEET })).toBeInTheDocument()
  })
})
