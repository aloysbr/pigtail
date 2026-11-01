import { act, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { makeBacklog } from '@/test/backlog'
import { groupButton, renderApp } from '@/test/render'

describe('Refresh', () => {
  it('a re-read keeps the selection, the folds and the scroll position', async () => {
    const data = makeBacklog()
    const { user, refetch } = await renderApp(data)
    await user.click(groupButton('th:th_pig'))
    await user.click(document.querySelector('li[data-id="pig-r2"]') as HTMLElement)
    await user.click(groupButton('sp:perso/Garden'))
    const scroller = document.querySelector('[data-scroll="list"]') as HTMLElement
    scroller.scrollTop = 120

    const next = makeBacklog()
    next.subjects = next.subjects.map((s) => (s.id === 'pig-i1' ? { ...s, status: 'parked' } : s))
    await act(() => refetch(next))
    // TanStack notifies observers on a later tick: wait for the new read to be drawn
    await waitFor(() => expect(document.querySelector('li[data-id="pig-i1"]')).toBeNull())

    expect(document.querySelector('[data-scroll="list"]')).toBe(scroller)
    expect(scroller.scrollTop).toBe(120)
    expect(document.querySelector('li.lr6.sel')).toHaveAttribute('data-id', 'pig-r2')
    expect(groupButton('th:th_pig')).toHaveAttribute('aria-expanded', 'true')
    expect(groupButton('sp:perso/Garden')).toHaveAttribute('aria-expanded', 'false')
    expect(document.querySelector('li[data-id="pig-i1"]')).toBeNull()
    expect(groupButton('th:th_pig:standby')).toHaveTextContent('3')
  })
})
