import { screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderApp } from '@/test/render'

/** jsdom lays nothing out: give the menu wrappers and panels the rects the browser measured (verify t2, 820 px, by finger). */
function stubRects(wrap: { left: number; right: number }, menuW: number, list?: { left: number; right: number }) {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const r = (left: number, width: number) => ({ left, right: left + width, width, top: 0, bottom: 32, height: 32, x: left, y: 0, toJSON: () => ({}) })
    if (this.classList.contains('gmenu')) return r(0, menuW) as DOMRect
    if (this.classList.contains('gmenu-w')) return r(wrap.left, wrap.right - wrap.left) as DOMRect
    if (this.classList.contains('lmain')) return (list ? r(list.left, list.right - list.left) : r(0, 0)) as DOMRect
    return r(0, 0) as DOMRect
  })
}

describe('header menus stay inside the window (verify t2)', () => {
  afterEach(() => vi.restoreAllMocks())

  it('Filtre near the right edge at 820 px: its menu ends --sp-6 from the edge', async () => {
    const { user } = await renderApp(undefined, { width: 820 })
    stubRects({ left: 741.5, right: 804 }, 220)
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    const menu = screen.getByRole('menu', { name: 'Filtrer' })
    // 820 - 16 - 220 = 584 in the window, so 584 - 741.5 from the button
    expect(menu.style.left).toBe('-157.5px')
    expect(menu.style.right).toBe('auto')
  })

  it('Filtre on the left: the left anchor holds', async () => {
    const { user } = await renderApp(undefined, { width: 1000 })
    stubRects({ left: 300, right: 362 }, 220)
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    expect(screen.getByRole('menu', { name: 'Filtrer' }).style.left).toBe('0px')
  })

  it('Grouper and Scopes use the same guard', async () => {
    const { user } = await renderApp(undefined, { width: 820 })
    stubRects({ left: 100, right: 160 }, 220)
    await user.click(within(document.querySelector('header') as HTMLElement).getByRole('button', { name: /Grouper/ }))
    // right anchor would start at 160 - 220 = -60: moved to the left margin, 16 - 100
    expect(screen.getByRole('menu', { name: 'Grouper par' }).style.left).toBe('-84px')
    await user.click(within(document.querySelector('header') as HTMLElement).getByRole('button', { name: /Scopes · / }))
    // left anchor at 100 ends at 320 <= 400 - 16: no shift
    expect(screen.getByRole('menu', { name: 'Scopes affichés' }).style.left).toBe('0px')
  })

  it('the sheet beside the list (window 1000, list 0-440, sheet from x 441): the Filtre menu stays inside the list, its width capped to it', async () => {
    const { user } = await renderApp(undefined, { width: 1000 })
    stubRects({ left: 339.5, right: 402 }, 220, { left: 0, right: 440 })
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    const menu = screen.getByRole('menu', { name: 'Filtrer' })
    // 440 - 16 - 220 = 204 in the window, so 204 - 339.5 from the button
    expect(menu.style.left).toBe('-135.5px')
    expect(menu.style.maxWidth).toBe('408px')
  })
})
