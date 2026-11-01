import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fireEvent } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { makeBacklog } from '@/test/backlog'
import { groupButton, renderApp } from '@/test/render'

function row(id: string): HTMLElement {
  const li = document.querySelector<HTMLElement>(`li[data-id="${id}"]`)
  if (!li) throw new Error(`no row ${id}`)
  return li
}

describe('Row', () => {
  it('#N only on a ranked Ouvert', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    await user.click(groupButton('th:th_pig:standby'))
    expect(row('pig-r1').querySelector('.rk6')).toHaveTextContent('#1')
    expect(row('pig-r2').querySelector('.rk6')).toHaveTextContent('#2')
    expect(row('pig-i1').querySelector('.rk6')).toHaveTextContent('')
    expect(row('pig-p1').querySelector('.rk6')).toHaveTextContent('')
  })

  it('shows the phase mark and its label, and the age', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    expect(row('pig-r1').querySelector('.lr-st')).toHaveTextContent('Construire')
    expect(row('pig-r1').querySelectorAll('.steps3 i.done')).toHaveLength(2)
    expect(row('pig-r1').querySelector('.steps3 i.cur')).not.toBeNull()
    expect(row('pig-r2').querySelector('.lr-st')).toHaveTextContent('Concevoir')
    expect(row('pig-u1').querySelector('.lr-st')).toHaveTextContent('Comprendre')
    expect(row('pig-i1').querySelector('.lr-st')).toHaveTextContent('')
    expect(row('pig-r1').querySelector('.lr-d')).toHaveTextContent('30 min')
    expect(row('pig-u1').querySelector('.lr-d')).toHaveTextContent('1 h')
    expect(row('pig-i1').querySelector('.lr-d')).toHaveTextContent('1 j')
  })

  it('the status icon says its label; an unknown stored status keeps its raw value in the tooltip', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    expect(row('pig-live').querySelector('.si')).toHaveAttribute('aria-label', 'En cours')
    expect(row('pig-r1').querySelector('.si')).toHaveAttribute('aria-label', 'Ouvert')
    expect(row('pig-r1').querySelector('.si')).toHaveAttribute('title', 'Statut stocké : building')
  })

  it('the project chip shows in the Statut and Scope groupings only', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    expect(row('pig-r1').querySelector('.lr-th .chip')).toBeNull()

    fireEvent.keyDown(document.body, { key: 'v' })
    fireEvent.keyDown(document.body, { key: "'", code: 'Digit4' })
    expect(row('pig-r1').querySelector('.lr-th .chip')).toHaveTextContent('pigtail')

    fireEvent.keyDown(document.body, { key: 'v' })
    fireEvent.keyDown(document.body, { key: '&', code: 'Digit1' })
    await user.click(groupButton('sp:perso/Allotment'))
    expect(row('pig-r1').querySelector('.lr-th .chip')).toHaveTextContent('pigtail')

    fireEvent.keyDown(document.body, { key: 'v' })
    fireEvent.keyDown(document.body, { key: '"', code: 'Digit3' })
    await user.click(groupButton('th:th_pig'))
    expect(row('pig-r1').querySelector('.lr-th .chip')).toBeNull()
  })

  it('a click selects the row', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    await user.click(row('pig-r2'))
    expect(row('pig-r2')).toHaveClass('sel')
    expect(row('pig-r2')).toHaveAttribute('aria-current', 'true')
  })

  it('D15: no stored short title -> the full title in bold, no grey long title, no 28-char cut', async () => {
    const data = makeBacklog()
    const subject = data.subjects.find((x) => x.id === 'pig-i1')!
    subject.short_title = undefined
    subject.title = 'Un titre assez long pour dépasser largement les vingt-huit caractères'
    const { user } = await renderApp(data)
    await user.click(groupButton('th:th_pig'))
    await user.click(groupButton('th:th_pig:standby'))
    const t = row('pig-i1').querySelector('.lr-t') as HTMLElement
    expect(t.querySelector('b')?.textContent).toBe(subject.title)
    expect(t.querySelector('.lr-full')).toBeNull()
  })

  describe('lot 3: the meta line (.lr-m) wraps the step and the chip, never a copy', () => {
    const meta = (id: string) => row(id).querySelector('.lr-m') as HTMLElement

    it('step and project share .lr-m', async () => {
      const { user } = await renderApp()
      await user.click(groupButton('th:th_pig'))
      fireEvent.keyDown(document.body, { key: 'v' })
      fireEvent.keyDown(document.body, { key: "'", code: 'Digit4' })
      const m = meta('pig-r1')
      expect(m.querySelector(':scope > .lr-st')).toHaveTextContent('Construire')
      expect(m.querySelector(':scope > .lr-th .chip')).toHaveTextContent('pigtail')
      // one copy only: the step and the chip are not repeated outside .lr-m
      expect(row('pig-r1').querySelectorAll('.lr-st')).toHaveLength(1)
      expect(row('pig-r1').querySelectorAll('.chip')).toHaveLength(1)
      expect(row('pig-r1')).not.toHaveClass('no-m')
    })

    it('Aire › Projet shows the step only', async () => {
      const { user } = await renderApp()
      await user.click(groupButton('th:th_pig'))
      const m = meta('pig-r1')
      expect(m.querySelector('.lr-st')).toHaveTextContent('Construire')
      expect(m.querySelector('.chip')).toBeNull()
      expect(row('pig-r1')).not.toHaveClass('no-m')
    })

    it('a row with neither is .no-m', async () => {
      const { user } = await renderApp()
      await user.click(groupButton('th:th_pig'))
      await user.click(groupButton('th:th_pig:standby'))
      expect(row('pig-i1')).toHaveClass('no-m')
      expect(row('pig-r1')).not.toHaveClass('no-m')
    })

    it('Projet grouping shows the step only', async () => {
      const { user } = await renderApp()
      fireEvent.keyDown(document.body, { key: 'v' })
      fireEvent.keyDown(document.body, { key: '"', code: 'Digit3' })
      await user.click(groupButton('th:th_pig'))
      const m = meta('pig-r1')
      expect(m.querySelector('.lr-st')).toHaveTextContent('Construire')
      expect(m.querySelector('.chip')).toBeNull()
    })

    it('Scope grouping shows step and chip', async () => {
      const { user } = await renderApp()
      fireEvent.keyDown(document.body, { key: 'v' })
      fireEvent.keyDown(document.body, { key: '&', code: 'Digit1' })
      await user.click(groupButton('sp:perso/Allotment'))
      const m = meta('pig-r1')
      expect(m.querySelector('.lr-st')).toHaveTextContent('Construire')
      expect(m.querySelector('.chip')).toHaveTextContent('pigtail')
    })
  })

  it('En cours and Ouvert rows are draggable with a mouse; Standby and Done rows are not', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    await user.click(groupButton('th:th_pig:standby'))
    await user.click(groupButton('th:th_pig:done'))
    expect(row('pig-live')).toHaveAttribute('draggable', 'true')
    expect(row('pig-r1')).toHaveAttribute('draggable', 'true')
    expect(row('pig-i1')).toHaveAttribute('draggable', 'true')
    expect(row('pig-p1')).not.toHaveAttribute('draggable', 'true')
    expect(row('pig-d1')).not.toHaveAttribute('draggable', 'true')
  })

  it('no row is draggable at 390 px (phone)', async () => {
    const { user } = await renderApp(makeBacklog(), { width: 390 })
    await user.click(groupButton('th:th_pig'))
    expect(row('pig-r1')).not.toHaveAttribute('draggable', 'true')
  })
})

describe('the age column (verify V3)', () => {
  it('a wider age (« à l\'instant ») keeps its right edge on the column: the cell is end-aligned, it never runs past the row', () => {
    const css = readFileSync(join(process.cwd(), 'src/list/list.css'), 'utf8')
    expect(css).toMatch(/\.lr6 \.lr-d \{[^}]*justify-self: end/)
  })
})

describe('the row’s « Lancer l » (lot 3)', () => {
  it('on Ouvert and Standby, in the row’s last column; none on En cours', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    await user.click(groupButton('th:th_pig:standby'))
    expect(row('pig-r1').querySelector('.lr-l button.launch-btn')).toHaveTextContent('Lancer')
    expect(row('pig-p1').querySelector('.lr-l button.launch-btn')).toHaveTextContent('Lancer')
    expect(row('pig-live').querySelector('.lr-l')).toBeEmptyDOMElement()
  })

  it('the grid gives every row the launch column (--size-launch-w), so ages line up', () => {
    const css = readFileSync(join(process.cwd(), 'src/list/list.css'), 'utf8')
    expect(css).toMatch(/\n  \.lr6 \{[^}]*grid-template-columns:[^;]*var\(--size-col-age\) var\(--size-launch-w\)/)
  })
})
