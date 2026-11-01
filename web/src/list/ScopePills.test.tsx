import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { resetUi } from '@/state/ui'
import { renderApp } from '@/test/render'

function pills() {
  return within(screen.getByRole('group', { name: 'Scopes affichés' }))
}

describe('ScopePills', () => {
  it('lists the scopes from the data with their counts, all on by default', async () => {
    await renderApp()
    const perso = pills().getByRole('button', { name: /perso/ })
    const ts = pills().getByRole('button', { name: /globex/ })
    expect(perso).toHaveAttribute('aria-pressed', 'true')
    expect(ts).toHaveAttribute('aria-pressed', 'true')
    expect(perso).toHaveTextContent('16')
    expect(ts).toHaveTextContent('2')
  })

  it('switching one off hides its subjects; the last one on cannot be switched off', async () => {
    const { user } = await renderApp()
    await user.click(pills().getByRole('button', { name: /globex/ }))
    expect(pills().getByRole('button', { name: /globex/ })).toHaveAttribute('aria-pressed', 'false')
    expect([...document.querySelectorAll('h2.gh .gname')].map((e) => e.textContent)).not.toContain('Build')
    const perso = pills().getByRole('button', { name: /perso/ })
    expect(perso).toHaveAttribute('aria-disabled', 'true')
    await user.click(perso)
    expect(perso).toHaveAttribute('aria-pressed', 'true')
  })

  it('keeps the choice across a reload (localStorage)', async () => {
    const first = await renderApp()
    await first.user.click(pills().getByRole('button', { name: /globex/ }))
    first.unmount()
    resetUi()
    await renderApp()
    expect(pills().getByRole('button', { name: /globex/ })).toHaveAttribute('aria-pressed', 'false')
  })

  it('a blocked localStorage does not break the pills', async () => {
    const orig = Object.getOwnPropertyDescriptor(window, 'localStorage')
    Object.defineProperty(window, 'localStorage', { configurable: true, get: () => { throw new Error('blocked') } })
    try {
      resetUi()
      const { user } = await renderApp()
      await user.click(pills().getByRole('button', { name: /globex/ }))
      expect(pills().getByRole('button', { name: /globex/ })).toHaveAttribute('aria-pressed', 'false')
    } finally {
      if (orig) Object.defineProperty(window, 'localStorage', orig)
    }
  })
})
