import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { CheckboxGroup } from './CheckboxGroup'

const OPTIONS = [
  { value: 'Apple', label: 'Apple', description: 'a' },
  { value: 'Banana', label: 'Banana' },
]

function Harness() {
  const [v, setV] = useState<string[]>([])
  return (
    <>
      <CheckboxGroup legend="Which fruits?" options={OPTIONS} value={v} onChange={setV} />
      <output>{v.join('|')}</output>
    </>
  )
}

describe('CheckboxGroup', () => {
  it('is a group named by its legend; each box is labelled by its option', () => {
    render(<Harness />)
    const g = screen.getByRole('group', { name: 'Which fruits?' })
    expect(g.querySelectorAll('input[type="checkbox"]')).toHaveLength(2)
    expect(screen.getByRole('checkbox', { name: 'Apple' })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Banana' })).toBeInTheDocument()
  })

  it('is controlled: a click reports the new list, in the order of the options', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByRole('checkbox', { name: 'Banana' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Apple' }))
    expect(screen.getByRole('status')).toHaveTextContent('Apple|Banana')
    expect(screen.getByRole('checkbox', { name: 'Apple' })).toBeChecked()
    await userEvent.click(screen.getByRole('checkbox', { name: 'Apple' }))
    expect(screen.getByRole('status')).toHaveTextContent('Banana')
  })

  it('keyboard: Tab reaches each box, Space toggles it', async () => {
    render(<Harness />)
    await userEvent.tab()
    expect(screen.getByRole('checkbox', { name: 'Apple' })).toHaveFocus()
    await userEvent.keyboard(' ')
    expect(screen.getByRole('checkbox', { name: 'Apple' })).toBeChecked()
    await userEvent.tab()
    expect(screen.getByRole('checkbox', { name: 'Banana' })).toHaveFocus()
  })
})
