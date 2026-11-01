import { act, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { setUi } from '@/state/ui'
import { groupButton, renderApp } from '@/test/render'

describe('GroupHeader', () => {
  it('shows its count and a summary of the statuses', async () => {
    await renderApp()
    const h = groupButton('sp:perso/Allotment')
    expect(within(h).getByTestId('count')).toHaveTextContent('13')
    expect(within(h).getByText('aire · perso')).toBeInTheDocument()
    const sum = [...h.querySelectorAll('.gs')].map((e) => e.textContent)
    expect(sum).toEqual(['1En cours', '8Ouvert', '2Standby', '2Done'])
  })

  it('folds and unfolds on click', async () => {
    const { user } = await renderApp()
    const h = groupButton('sp:perso/Allotment')
    expect(h).toHaveAttribute('aria-expanded', 'true')
    expect(document.querySelector('button[data-g="th:th_pig"]')).not.toBeNull()
    await user.click(h)
    expect(h).toHaveAttribute('aria-expanded', 'false')
    expect(document.querySelector('button[data-g="th:th_pig"]')).toBeNull()
    await user.click(h)
    expect(h).toHaveAttribute('aria-expanded', 'true')
  })

  it('marks an ongoing project « continu »', async () => {
    await renderApp()
    expect(groupButton('th:th_meth')).toHaveTextContent('projet · continu')
    expect(groupButton('th:th_pig')).not.toHaveTextContent('continu')
  })

  it('the project pie reads the sidebar base: a status filter does not change « n % fait »', async () => {
    const { user } = await renderApp()
    const before = groupButton('th:th_pig').querySelector('.gh-pie')?.getAttribute('aria-label') ?? groupButton('th:th_pig').querySelector('[role="img"]')?.getAttribute('aria-label')
    expect(before).toMatch(/% fait/)
    act(() => setUi({ statusF: 'cours' }))
    const gb = groupButton('th:th_pig')
    const after = gb.querySelector('[role="img"]')?.getAttribute('aria-label')
    expect(after).toBe(before)
    void user
  })
})
