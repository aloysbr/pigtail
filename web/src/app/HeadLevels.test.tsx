import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { resetWidths } from '@/state/widths'
import { renderApp, setWidth } from '@/test/render'

const head = () => document.querySelector('header.d-head') as HTMLElement

/** t10 hdLevel(): the header folds by the list's available width (window minus the docked sidebar). */
describe('Header levels (t10 --bp-head-1 / --bp-head-2 / --bp-head-tight)', () => {
  it('level 0 from 1000 px of list: scope pills; « tight » (no counts) under 1180', async () => {
    await renderApp(undefined, { width: 1440 }) // docked: 1440 - 240 = 1200
    expect(head()).toHaveClass('hd-0')
    expect(head()).not.toHaveClass('tight')
    expect(within(head()).getByRole('group', { name: 'Scopes affichés' })).toBeInTheDocument()
  })

  it('1024 (no sidebar, list 1024): level 0, tight', async () => {
    await renderApp(undefined, { width: 1024 })
    expect(head()).toHaveClass('hd-0', 'tight')
  })

  it('level 1 (920-1000): the scopes fold into one menu', async () => {
    const { user } = await renderApp(undefined, { width: 1160 }) // docked: 920
    expect(head()).toHaveClass('hd-1')
    expect(within(head()).queryByRole('group', { name: 'Scopes affichés' })).toBeNull()
    await user.click(within(head()).getByRole('button', { name: /Scopes · 2\/2/ }))
    const menu = screen.getByRole('menu', { name: 'Scopes affichés' })
    await user.click(within(menu).getByRole('menuitemcheckbox', { name: /globex/ }))
    expect(within(head()).getByRole('button', { name: /Scopes · 1\/2/ })).toBeInTheDocument()
  })

  it('level 2 (< 920): group and abandoned fold to icons, « sujets » is visually hidden', async () => {
    await renderApp(undefined, { width: 700 })
    expect(head()).toHaveClass('hd-2')
    const g = within(head()).getByRole('button', { name: /Grouper : / })
    expect(g.textContent).not.toMatch(/Grouper/)
    expect(g.querySelector('svg')).not.toBeNull()
    const d = within(head()).getByRole('button', { name: /abandonnés/i })
    expect(d.textContent).not.toMatch(/Abandonnés/)
    expect(head().querySelector('.fcount .sr')).not.toBeNull()
  })

  it('reacts to a resize and to the sidebar docking', async () => {
    await renderApp(undefined, { width: 1440 })
    expect(head()).toHaveClass('hd-0')
    act(() => setWidth(1160)) // still docked: 920
    expect(head()).toHaveClass('hd-1')
    act(() => setWidth(1159)) // docked: 919, under the level-2 threshold
    expect(head()).toHaveClass('hd-2')
    act(() => setWidth(930)) // not dockable: list 930
    expect(head()).toHaveClass('hd-1')
    act(() => setWidth(740))
    expect(head()).toHaveClass('hd-2')
  })

  it('the loading header uses the same level as the loaded one', async () => {
    for (const w of [1440, 1024, 900, 700]) {
      const lines = () => [!!head().querySelector(':scope > .h-l1'), !!head().querySelector(':scope > .h-l2')]
      const a = await renderApp(undefined, { width: w, wait: false })
      const loading = [...head().classList].filter((c) => /^hd-|^tight$|^h2l$/.test(c)).sort()
      const loadingLines = lines()
      a.unmount()
      const b = await renderApp(undefined, { width: w })
      const loaded = [...head().classList].filter((c) => /^hd-|^tight$|^h2l$/.test(c)).sort()
      const loadedLines = lines()
      b.unmount()
      expect(loading.length).toBeGreaterThan(0)
      expect(loading).toEqual(loaded)
      // at hd-2 both lines are there while loading as once loaded (the header keeps its height)
      const two = loaded.includes('hd-2')
      expect(loadingLines).toEqual([two, two])
      expect(loadedLines).toEqual([two, two])
    }
  })

  it('level 2 is two fixed lines (D7, Q3 A)', async () => {
    const { user } = await renderApp(undefined, { width: 700 })
    expect(head()).toHaveClass('d-head', 'hd-2', 'h2l')
    const l1 = head().querySelector(':scope > .h-l1') as HTMLElement
    const l2 = head().querySelector(':scope > .h-l2') as HTMLElement
    expect(l1).not.toBeNull()
    expect(l2).not.toBeNull()
    expect(within(l1).getByRole('heading', { name: 'Backlog' })).toBeInTheDocument()
    // the Scopes label sits in its own .gm-l, the part of line 1 that ellipsizes in a narrow list
    expect(within(l1).getByRole('button', { name: /Scopes · / }).querySelector('.gm-l')).toHaveTextContent(/^Scopes · \d+\/\d+$/)
    expect(l1.querySelector('.fcount')).not.toBeNull()
    expect(within(l1).getByRole('button', { name: /Grouper : / })).toBeInTheDocument()
    const input = within(l2).getByRole('searchbox', { name: 'Rechercher dans le backlog' })
    expect(within(l2).getByRole('button', { name: /Filtre/ })).toBeInTheDocument()
    expect(l1.querySelector('.fsearch')).toBeNull()
    // no filter yet: no chip group, so no « +n »
    expect(head().querySelector('.h-more:not([hidden])')).toBeNull()
    await user.type(input, 'atlas')
    const chips = l2.querySelector(':scope > .h-chips') as HTMLElement
    expect(chips).not.toBeNull()
    expect(within(chips).getByRole('button', { name: 'Retirer le filtre Recherche' })).toBeInTheDocument()
    // « Effacer tous les filtres » is the last chip, so the first one folded
    const items = [...chips.querySelectorAll(':scope > .h-ch')]
    expect(items.at(-1)).toHaveTextContent('Effacer tous les filtres')
    // nothing measures in jsdom: nothing folds, « +n » stays hidden
    const more = chips.querySelector('.h-more')
    expect(more === null || more.hasAttribute('hidden')).toBe(true)
    // the search takes the line: its focus never widens it
    act(() => input.focus())
    expect(document.querySelector('.fsearch')).not.toHaveClass('wide')
  })

  it('a level change keeps the search focus and caret (⌘B while typing, docked at 1159)', async () => {
    const { user } = await renderApp(undefined, { width: 1159 }) // docked: 919, level 2
    expect(head()).toHaveClass('hd-2')
    const input = () => screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }) as HTMLInputElement
    await user.click(input())
    await user.keyboard('ab')
    fireEvent.keyDown(input(), { key: 'b', code: 'KeyB', metaKey: true }) // undocks: the list is 1159, one line (level 0)
    expect(head()).toHaveClass('hd-0')
    expect(document.activeElement).toBe(input())
    expect(input().selectionStart).toBe(2)
    await user.keyboard('c')
    expect(input()).toHaveValue('abc')
    fireEvent.keyDown(input(), { key: 'b', code: 'KeyB', metaKey: true }) // docks again: level 2
    expect(head()).toHaveClass('hd-2')
    expect(document.activeElement).toBe(input())
    expect(input().selectionStart).toBe(3)
  })

  it('above 920 one line as today', async () => {
    for (const w of [1440, 1160]) {
      const { user, unmount } = await renderApp(undefined, { width: w })
      expect(head()).toHaveClass(w === 1440 ? 'hd-0' : 'hd-1')
      expect(head()).not.toHaveClass('h2l')
      expect(head().querySelector('.h-l1, .h-l2, .h-chips, .h-more')).toBeNull()
      await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'atlas')
      expect(head().querySelector('.h-chips, .h-more')).toBeNull()
      expect(within(head()).getByRole('button', { name: 'Retirer le filtre Recherche' }).closest('.fchip')?.parentElement).toBe(head())
      unmount()
    }
  })

  it('the phone layout is kept: no fold', async () => {
    await renderApp(undefined, { width: 390 })
    expect(head()).toHaveClass('hd-0')
    expect(head()).not.toHaveClass('tight')
  })

  it('the header fold follows the sidebar width', async () => {
    // 1200 docked: the default 240 px sidebar leaves 960 (level 1), a 200 px one leaves 1000 (level 0)
    const a = await renderApp(undefined, { width: 1200 })
    expect(head()).toHaveClass('hd-1')
    a.unmount()
    localStorage.setItem('pigtail-side-w', '200')
    resetWidths()
    await renderApp(undefined, { width: 1200 })
    expect(head()).toHaveClass('hd-0')
  })
})

describe('Search field widening (t10: siblings reflow; a click on the right lands first time)', () => {
  const field = () => document.querySelector('.fsearch') as HTMLElement
  const input = () => screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })

  it('is wide while focused and shrinks on a plain blur', async () => {
    await renderApp()
    expect(field()).not.toHaveClass('wide')
    act(() => input().focus())
    expect(field()).toHaveClass('wide')
    act(() => input().blur())
    await waitFor(() => expect(field()).not.toHaveClass('wide'))
  })

  // the browser's default action of a mousedown: it blurs the focused input unless prevented
  const mouseDown = (target: Element) => {
    const proceed = fireEvent.mouseDown(target)
    if (proceed) act(() => input().blur())
  }

  it('a finger tap on a chip x with the search focused: pointerdown, touchstart, pointerup, touchend, mousedown+blur, mouseup, click', async () => {
    const { user } = await renderApp()
    await user.type(input(), 'atlas')
    const x = screen.getByRole('button', { name: 'Retirer le filtre Recherche' })
    fireEvent.pointerDown(x)
    fireEvent.touchStart(x)
    fireEvent.pointerUp(x)
    fireEvent.touchEnd(x)
    mouseDown(x)
    // the compatibility mousedown comes after pointerup: shrinking now would slide the chips from under the finger
    await new Promise((r) => setTimeout(r, 30))
    expect(field()).toHaveClass('wide')
    fireEvent.mouseUp(x)
    fireEvent.click(x)
    expect(input()).toHaveValue('')
  })

  it('a mouse click on a chip x with the search focused: pointerdown, mousedown+blur, pointerup, mouseup, click', async () => {
    const { user } = await renderApp()
    await user.type(input(), 'atlas')
    const x = screen.getByRole('button', { name: 'Retirer le filtre Recherche' })
    fireEvent.pointerDown(x)
    mouseDown(x)
    expect(field()).toHaveClass('wide')
    fireEvent.pointerUp(x)
    fireEvent.mouseUp(x)
    fireEvent.click(x)
    expect(input()).toHaveValue('')
  })

  it('a tap on the empty background with the search focused: the field shrinks', async () => {
    await renderApp()
    act(() => input().focus())
    fireEvent.pointerDown(head())
    fireEvent.pointerUp(head())
    mouseDown(head())
    await waitFor(() => expect(field()).not.toHaveClass('wide'), { timeout: 1000 })
  })

  it('a click on a button right of the field lands: its position is unchanged until the press ends', async () => {
    const { user } = await renderApp()
    await user.type(input(), 'atlas')
    await user.click(screen.getByRole('button', { name: 'Effacer tous les filtres' }))
    expect(input()).toHaveValue('')
  })
})
