import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { Segment } from './Segment'

const OPTS = [
  { value: 'light', label: 'Clair' },
  { value: 'dark', label: 'Sombre' },
  { value: 'system', label: 'Système' },
] as const

function Harness({ onChange = () => {}, start = 'light' }: { onChange?: (v: string) => void; start?: string }) {
  const [v, setV] = useState(start)
  return (
    <>
      <h3 id="lbl">Apparence</h3>
      <Segment
        labelledBy="lbl"
        options={OPTS}
        value={v}
        onChange={(n) => {
          setV(n)
          onChange(n)
        }}
      />
    </>
  )
}

describe('Segment', () => {
  it('is a radiogroup named by its label, one radio checked', () => {
    render(<Harness />)
    const g = screen.getByRole('radiogroup', { name: 'Apparence' })
    expect(g).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Clair' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('radio', { name: 'Sombre' })).toHaveAttribute('aria-checked', 'false')
  })

  it('the arrows move the value and the focus, wrapping around', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<Harness onChange={onChange} />)
    screen.getByRole('radio', { name: 'Clair' }).focus()
    await user.keyboard('{ArrowRight}')
    expect(onChange).toHaveBeenLastCalledWith('dark')
    expect(screen.getByRole('radio', { name: 'Sombre' })).toHaveFocus()
    await user.keyboard('{ArrowDown}{ArrowRight}')
    expect(onChange).toHaveBeenLastCalledWith('light')
    await user.keyboard('{ArrowLeft}')
    expect(onChange).toHaveBeenLastCalledWith('system')
    expect(screen.getByRole('radio', { name: 'Système' })).toHaveAttribute('aria-checked', 'true')
  })

  it('Space picks the focused radio', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<Harness onChange={onChange} />)
    screen.getByRole('radio', { name: 'Système' }).focus()
    await user.keyboard(' ')
    expect(onChange).toHaveBeenLastCalledWith('system')
  })

  it('only the checked radio is in the tab order', () => {
    render(<Harness start="dark" />)
    expect(screen.getByRole('radio', { name: 'Sombre' })).toHaveAttribute('tabindex', '0')
    expect(screen.getByRole('radio', { name: 'Clair' })).toHaveAttribute('tabindex', '-1')
  })
})
