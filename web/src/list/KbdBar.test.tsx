import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { openPanel, resetUi, setUi } from '@/state/ui'
import { withCaptures } from '@/test/backlog'
import { renderApp } from '@/test/render'
import { pushToast, resetToasts } from '@/ui/toasts'

afterEach(() => resetToasts())

function items(): (string | null)[] {
  const bar = screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })
  return [...bar.querySelectorAll('.kb')].map((e) => e.textContent)
}

describe('Shortcut footer', () => {
  it('lists the keys; « , masquer » hides it, « , raccourcis » brings it back; the choice survives a reload', async () => {
    const first = await renderApp()
    const bar = screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })
    expect([...bar.querySelectorAll('.kb')].map((e) => e.textContent)).toEqual(['ccapturer', 'jkligne', '⏎ouvrir', '←→replier / déplier', 'g1…9aire', 'vgrouper', 'ffiltrer', ',masquer'])
    await first.user.click(screen.getByRole('button', { name: /masquer/ }))
    expect(screen.queryByRole('contentinfo', { name: 'Raccourcis clavier' })).toBeNull()
    expect(screen.getByRole('button', { name: /raccourcis/ })).toBeInTheDocument()

    first.unmount()
    resetUi()
    const second = await renderApp()
    expect(screen.queryByRole('contentinfo', { name: 'Raccourcis clavier' })).toBeNull()
    await second.user.click(screen.getByRole('button', { name: /raccourcis/ }))
    expect(screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })).toBeInTheDocument()
  })

  it('then and ellipsis are span.k-then, never a kbd', async () => {
    await renderApp()
    const bar = screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })
    expect([...bar.querySelectorAll('kbd')].map((k) => k.textContent)).not.toContain('…')
    expect([...bar.querySelectorAll('span.k-then')].map((k) => k.textContent)).toEqual(['…'])
  })
})

describe('Shortcut footer: the selected line’s actions (lot 3)', () => {
  const NAV = ['jkligne', '⏎ouvrir', '←→replier / déplier', 'g1…9aire', 'vgrouper', 'ffiltrer']

  it('Ouvert: l lancer, h standby, ⌥↑ ⌥↓ rang, c capturer, then the navigation, « , masquer » last', async () => {
    await renderApp()
    act(() => setUi({ sel: 'pig-r1' }))
    expect(items()).toEqual(['llancer', 'hstandby', '⌥↑⌥↓rang', 'ccapturer', ...NAV, ',masquer'])
  })

  it('En cours: no l; h standby, ⌥↑ ⌥↓ rang, c capturer', async () => {
    await renderApp()
    act(() => setUi({ sel: 'pig-live' }))
    expect(items()).toEqual(['hstandby', '⌥↑⌥↓rang', 'ccapturer', ...NAV, ',masquer'])
  })

  it('Standby: l lancer, h reprendre, no rang', async () => {
    await renderApp()
    act(() => setUi({ sel: 'pig-p1' }))
    expect(items()).toEqual(['llancer', 'hreprendre', 'ccapturer', ...NAV, ',masquer'])
  })

  it('Done, and a capture: c capturer only', async () => {
    await renderApp(withCaptures())
    act(() => setUi({ sel: 'pig-d1' }))
    expect(items()).toEqual(['ccapturer', ...NAV, ',masquer'])
    act(() => setUi({ sel: 'cap-b1' }))
    // ⏎ does nothing on a capture: no « ⏎ ouvrir » there
    expect(items()).toEqual(['ccapturer', 'jkligne', '←→replier / déplier', 'g1…9aire', 'vgrouper', 'ffiltrer', ',masquer'])
  })

  it('z annuler only while an undoable toast shows (a rank one included, D23)', async () => {
    await renderApp()
    act(() => setUi({ sel: 'pig-d1' }))
    act(() => void pushToast({ text: 'Session ouverte dans cmux' }))
    expect(items()).not.toContain('zannuler')
    act(() => void pushToast({ text: 'Rang de « Atlas web » modifié', undo: { title: 'Atlas web', label: 'Rang de « Atlas web » rétabli', run: async () => {}, stillValid: () => true } }))
    expect(items()).toEqual(['ccapturer', 'zannuler', ...NAV, ',masquer'])
    act(() => resetToasts())
    expect(items()).not.toContain('zannuler')
  })

  it('a narrow list (under 640 px beside the sheet) lists the same items: the CSS hides what does not fit, whole items', async () => {
    await renderApp(undefined, { width: 1200 })
    act(() => openPanel('pig-r1'))
    expect(items()).toEqual(['llancer', 'hstandby', '⌥↑⌥↓rang', 'ccapturer', ...NAV, ','])
  })

  it('narrow: « , » keeps its line, named « Masquer les raccourcis »', async () => {
    await renderApp(undefined, { width: 1200 })
    act(() => openPanel('pig-r1'))
    const bar = screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })
    expect(bar).toHaveClass('kbdbar-n')
    expect(screen.getByRole('button', { name: 'Masquer les raccourcis' })).toHaveTextContent(',')
  })
})

describe('Shortcut footer: one line at every width', () => {
  const css = readFileSync(join(process.cwd(), 'src/list/list.css'), 'utf8')
  const rule = (sel: string) => css.match(new RegExp(`\\n  ${sel.replace(/[.]/g, '\\.')} \\{([^}]*)\\}`))?.[1] ?? ''

  it('the items sit in their own single-line box: wrapped items fall out of a fixed one-line height, whole', async () => {
    await renderApp()
    const bar = screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })
    const box = bar.querySelector('.kb-items')
    expect(box).not.toBeNull()
    // the toggle stays outside the box: it is never the one that wraps away
    expect(box!.contains(screen.getByRole('button', { name: /masquer/ }))).toBe(false)
    const r = rule('.kb-items')
    expect(r).toMatch(/flex-wrap: wrap/)
    expect(r).toMatch(/height: var\(--size-target\)/)
    expect(r).toMatch(/overflow: hidden/)
    expect(r).toMatch(/align-content: flex-start/)
    // each item fills the one line, so the next line starts below the box
    expect(rule('.kb')).toMatch(/min-height: var\(--size-target\)/)
  })

  it('the bar itself never wraps, whatever the width', () => {
    expect(rule('.kbdbar')).not.toMatch(/flex-wrap: wrap/)
    expect(css).not.toMatch(/\.kbdbar\.kbdbar-n \{[^}]*flex-wrap/)
  })
})
