import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { groupButton, renderApp, rowIds } from '@/test/render'

describe('Project cap', () => {
  it('6 rows En cours + Ouvert per project, then « Afficher les N autres »', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    expect(rowIds()).toEqual(['pig-live', 'pig-r1', 'pig-r2', 'pig-u1', 'pig-i1', 'pig-i2'])
    await user.click(screen.getByRole('button', { name: 'Afficher les 2 autres' }))
    expect(rowIds()).toEqual(['pig-live', 'pig-r1', 'pig-r2', 'pig-u1', 'pig-i1', 'pig-i2', 'pig-i3', 'pig-i4'])
  })

  it('Standby and Done are folded on one line each: « Standby 2 », « Done 2 »', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    const sb = groupButton('th:th_pig:standby')
    const dn = groupButton('th:th_pig:done')
    expect(sb).toHaveAttribute('aria-expanded', 'false')
    expect(sb).toHaveTextContent(/Standby\s*2/)
    expect(dn).toHaveTextContent(/Done\s*2/)
    expect(rowIds()).not.toContain('pig-p1')
    await user.click(sb)
    expect(rowIds()).toEqual(expect.arrayContaining(['pig-p1', 'pig-p2']))
  })

  it('G5: a project group header shows its Pie before the name, from the group summary (done / total)', async () => {
    await renderApp()
    const head = groupButton('th:th_pig')
    const pie = head.querySelector('svg[role="img"]')
    expect(pie).toHaveAccessibleName('17 % fait')
    // before the name
    const name = head.querySelector('.gname') as Element
    expect(pie!.compareDocumentPosition(name) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
