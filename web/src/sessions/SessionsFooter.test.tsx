import { fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { getUi } from '@/state/ui'
import { withSessions } from '@/test/peers-fixture'
import { renderApp } from '@/test/render'

const key = (k: string, extra: Partial<KeyboardEventInit> = {}) => fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...extra })

describe('Sessions footer', () => {
  it('announces exactly the keys the view has, and each one does what it says', async () => {
    await renderApp(withSessions())
    key('s')
    await screen.findByRole('region', { name: 'Sessions' })
    const bar = screen.getByRole('contentinfo', { name: 'Raccourcis clavier' })
    const items = [...bar.querySelectorAll('.kb')].map((e) => {
      const keys = [...e.querySelectorAll('kbd')].map((k) => k.textContent).join(' ')
      const c = e.cloneNode(true) as HTMLElement
      c.querySelectorAll('kbd').forEach((k) => k.remove())
      return [keys, c.textContent?.trim()]
    })
    expect(items).toEqual([
      ['↑ ↓', 'Naviguer'],
      ['⏎', 'Détail'],
      ['/', 'Filtrer'],
      ['⌘K', 'Palette'],
    ])
    // the backlog's l (Lancer), h (Standby), f (Filtrer…) are not announced
    for (const k of ['l', 'h', 'f']) expect(within(bar).queryByText(k, { selector: 'kbd' })).toBeNull()

    key('ArrowDown')
    const sel = getUi().sessionSel
    expect(sel).not.toBeNull()
    key('Enter')
    expect(getUi().sessionPanel).toBe(true)
    key('Escape')
    expect(getUi().sessionPanel).toBe(false)
    key('/')
    expect(document.activeElement).toHaveAttribute('id', 'sessions-search')
    ;(document.activeElement as HTMLElement).blur()
    key('k', { code: 'KeyK', metaKey: true })
    expect(screen.getByRole('dialog', { name: 'Palette de commandes' })).not.toBeNull()
  })
})
