import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderApp, rowIds, unfoldAll } from '@/test/render'

describe('« Abandonnés · N »', () => {
  it('counts dropped + captured; showing reveals both, hiding again masks them', async () => {
    const { user } = await renderApp()
    const btn = screen.getByRole('button', { name: /Abandonnés · 3/ })
    expect(btn).toHaveAttribute('aria-pressed', 'false')
    unfoldAll()
    expect(rowIds()).not.toEqual(expect.arrayContaining(['pig-x1']))
    expect(rowIds()).not.toContain('pig-c1')

    await user.click(btn)
    expect(btn).toHaveAttribute('aria-pressed', 'true')
    unfoldAll()
    expect(rowIds()).toEqual(expect.arrayContaining(['pig-x1', 'pig-c1', 'wk-x']))
    expect(document.querySelector('li[data-id="pig-c1"] .si')).toHaveAttribute('aria-label', 'Capturé')
    expect(document.querySelector('li[data-id="pig-x1"] .si')).toHaveAttribute('aria-label', 'Abandonné')

    await user.click(btn)
    expect(rowIds()).not.toContain('pig-x1')
    expect(rowIds()).not.toContain('pig-c1')
  })

  it('when on, the label reads « Masquer les abandonnés · N »', async () => {
    const { user } = await renderApp()
    const btn = screen.getByRole('button', { name: /Abandonnés · 3/ })
    await user.click(btn)
    expect(btn).toHaveTextContent('Masquer les abandonnés · 3')
    expect(btn).toHaveAttribute('aria-pressed', 'true')
    await user.click(btn)
    expect(btn).toHaveTextContent(/^Abandonnés · 3$/)
  })
})
