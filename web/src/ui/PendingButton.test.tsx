import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { PendingButton } from './PendingButton'

describe('PendingButton', () => {
  it('at rest: its label and key, clickable, not busy', async () => {
    const onClick = vi.fn()
    render(<PendingButton pending={false} label="Lancer" busy="Lancement…" keys="l" onClick={onClick} className="launch-btn" />)
    const b = screen.getByRole('button', { name: 'Lancer' })
    expect(b).toHaveAttribute('aria-keyshortcuts', 'l')
    expect(b.querySelector('kbd')).toHaveTextContent('l')
    expect(b).not.toBeDisabled()
    expect(b).not.toHaveAttribute('aria-busy')
    expect(b).toHaveClass('btnl', 'launch-btn')
    expect(b.querySelector('svg.spin')).toBeNull()
    await userEvent.click(b)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('pending: « Lancement… » greyed with its spinner, aria-busy, a click does nothing', async () => {
    const onClick = vi.fn()
    render(<PendingButton pending label="Lancer" busy="Lancement…" keys="l" onClick={onClick} />)
    const b = screen.getByRole('button', { name: 'Lancement…' })
    expect(b).toBeDisabled()
    expect(b).toHaveAttribute('aria-busy', 'true')
    expect(b.querySelector('svg.spin')).not.toBeNull()
    expect(b.querySelector('kbd')).toBeNull()
    await userEvent.click(b)
    expect(onClick).not.toHaveBeenCalled()
  })
})

describe('PendingButton primary', () => {
  it('primary: the filled .btnp, not the bordered .btnl', () => {
    render(<PendingButton primary pending={false} label="Aller" busy="Aller" onClick={() => {}} />)
    const b = screen.getByRole('button', { name: 'Aller' })
    expect(b).toHaveClass('btnp')
    expect(b).not.toHaveClass('btnl')
  })
})
