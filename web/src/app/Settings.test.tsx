import { act, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getUi, openPanel } from '@/state/ui'
import { renderApp } from '@/test/render'

function fakeMedia(dark: boolean) {
  const mql = { matches: dark, media: '(prefers-color-scheme: dark)', addEventListener: () => {}, removeEventListener: () => {} }
  return (q: string) => (/prefers-color-scheme:\s*dark/.test(q) ? mql : { matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {} })
}

const root = () => document.documentElement
const dlg = () => screen.queryByRole('dialog', { name: 'Réglages' })
const key = (k: string, init: Partial<KeyboardEventInit> = {}) => fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...init })

beforeEach(() => {
  vi.stubGlobal('matchMedia', fakeMedia(false))
  root().removeAttribute('data-theme-palette')
  root().removeAttribute('data-theme')
})
afterEach(() => {
  vi.unstubAllGlobals()
  root().removeAttribute('data-theme-palette')
  root().removeAttribute('data-theme')
})

describe('Réglages (T8)', () => {
  it('the footer button « Réglages » of the sidebar opens the window', async () => {
    const { user } = await renderApp()
    expect(dlg()).toBeNull()
    const btn = within(screen.getByRole('complementary', { name: 'Navigation' })).getByRole('button', { name: /Réglages/ })
    expect(btn).toHaveAttribute('aria-haspopup', 'dialog')
    await user.click(btn)
    expect(dlg()).not.toBeNull()
    expect(getUi().settings).toBe(true)
  })

  it('⌘, opens and closes it, even typed in a field', async () => {
    await renderApp()
    key(',', { metaKey: true })
    expect(dlg()).not.toBeNull()
    key(',', { metaKey: true })
    expect(dlg()).toBeNull()
    const search = document.querySelector<HTMLInputElement>('input')!
    search.focus()
    key(',', { metaKey: true })
    expect(dlg()).not.toBeNull()
  })

  it('Ctrl+, does the same', async () => {
    await renderApp()
    key(',', { ctrlKey: true })
    expect(dlg()).not.toBeNull()
  })

  it('Esc closes it and leaves the sheet open under it', async () => {
    await renderApp()
    act(() => openPanel('pig-r1'))
    expect(document.querySelector('.lpanel')).not.toBeNull()
    key(',', { metaKey: true })
    expect(dlg()).not.toBeNull()
    key('Escape')
    expect(dlg()).toBeNull()
    expect(document.querySelector('.lpanel')).not.toBeNull()
  })

  it('the close cross closes it, Esc still does (no « Esc » key printed)', async () => {
    const { user } = await renderApp()
    key(',', { metaKey: true })
    expect(within(dlg()!).queryByText('Esc')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Fermer les réglages' }))
    expect(dlg()).toBeNull()
  })

  it('a click outside closes it, a click inside does not', async () => {
    const { user } = await renderApp()
    key(',', { metaKey: true })
    await user.click(screen.getByText('Thème'))
    expect(dlg()).not.toBeNull()
    fireEvent.pointerDown(document.querySelector('.lmain')!)
    expect(dlg()).toBeNull()
  })

  it('shows the t3 strings: two theme cards, the segment, the note and the footer', async () => {
    await renderApp()
    key(',', { metaKey: true })
    const d = within(dlg()!)
    expect(d.getByRole('button', { name: /Terra/ })).toHaveTextContent('Ivoire et terre cuite')
    expect(d.getByRole('button', { name: /Ocean/ })).toHaveTextContent('Gris pur et indigo')
    expect(d.getByRole('radiogroup', { name: 'Apparence' })).toBeInTheDocument()
    expect(d.getByRole('radio', { name: 'Clair' })).toBeInTheDocument()
    expect(d.getByRole('radio', { name: 'Sombre' })).toBeInTheDocument()
    expect(d.getByRole('radio', { name: 'Système' })).toBeInTheDocument()
    // toHaveTextContent folds nbsp into a plain space: the glue is read on the raw text
    expect(dlg()?.textContent).toContain('Enregistré sur cet appareil · aussi dans ⌘K «\u00a0thème\u00a0»')
  })

  it('the card of the current theme is pressed and takes the first focus; a click on Ocean applies and saves it', async () => {
    const { user } = await renderApp()
    key(',', { metaKey: true })
    const d = within(dlg()!)
    expect(d.getByRole('button', { name: /Terra/ })).toHaveAttribute('aria-pressed', 'true')
    expect(d.getByRole('button', { name: /Terra/ })).toHaveFocus()
    expect(d.getByRole('button', { name: /Ocean/ })).toHaveAttribute('aria-pressed', 'false')
    await user.click(d.getByRole('button', { name: /Ocean/ }))
    expect(root().getAttribute('data-theme-palette')).toBe('ocean')
    expect(localStorage.getItem('pigtail-palette')).toBe('ocean')
    expect(d.getByRole('button', { name: /Ocean/ })).toHaveAttribute('aria-pressed', 'true')
  })

  it('each card previews its own theme with the tokens: data-theme-palette and the resolved data-theme on the preview', async () => {
    await renderApp()
    key(',', { metaKey: true })
    const prev = [...dlg()!.querySelectorAll('.thprev')]
    expect(prev.map((p) => p.getAttribute('data-theme-palette'))).toEqual(['terra', 'ocean'])
    expect(prev.map((p) => p.getAttribute('data-theme'))).toEqual(['light', 'light'])
  })

  it('the segment sets data-theme and pigtail-appearance; Système notes what the Mac does now', async () => {
    const { user } = await renderApp()
    key(',', { metaKey: true })
    const d = within(dlg()!)
    expect(d.getByRole('radio', { name: 'Système' })).toHaveAttribute('aria-checked', 'true')
    expect(dlg()).toHaveTextContent('Suit le Mac : clair en ce moment.')
    await user.click(d.getByRole('radio', { name: 'Sombre' }))
    expect(root().getAttribute('data-theme')).toBe('dark')
    expect(localStorage.getItem('pigtail-appearance')).toBe('dark')
    expect(dlg()).toHaveTextContent('Système suit le réglage du Mac.')
    expect(dlg()).not.toHaveTextContent('Suit le Mac')
    await user.click(d.getByRole('radio', { name: 'Système' }))
    expect(root().getAttribute('data-theme')).toBe('light')
  })

  it('Système with a dark Mac says « sombre »', async () => {
    vi.stubGlobal('matchMedia', fakeMedia(true))
    await renderApp()
    key(',', { metaKey: true })
    expect(dlg()).toHaveTextContent('Suit le Mac : sombre en ce moment.')
  })

  it('« , » alone does nothing while open (the shortcut bar stays), and does not open it when closed', async () => {
    await renderApp()
    const kb = getUi().kbHidden
    key(',')
    expect(dlg()).toBeNull()
    expect(getUi().kbHidden).not.toBe(kb)
    key(',')
    key(',', { metaKey: true })
    const kb2 = getUi().kbHidden
    key(',')
    expect(getUi().kbHidden).toBe(kb2)
    expect(dlg()).not.toBeNull()
  })
})
