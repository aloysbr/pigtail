import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { TextField } from './TextField'

function Harness({ onSubmit }: { onSubmit?: (v: string) => void }) {
  const [v, setV] = useState('')
  return <TextField label="Motif" value={v} onChange={setV} onSubmit={onSubmit} />
}

describe('TextField', () => {
  it('its label is bound to the field', () => {
    render(<Harness />)
    expect(screen.getByLabelText('Motif')).toBe(screen.getByRole('textbox', { name: 'Motif' }))
  })

  it('is controlled', async () => {
    render(<Harness />)
    await userEvent.type(screen.getByRole('textbox', { name: 'Motif' }), 'trop risqué')
    expect(screen.getByRole('textbox', { name: 'Motif' })).toHaveValue('trop risqué')
  })

  it('keyboard: Enter submits a non empty value, an empty one does nothing', async () => {
    const onSubmit = vi.fn()
    render(<Harness onSubmit={onSubmit} />)
    await userEvent.type(screen.getByRole('textbox', { name: 'Motif' }), '{Enter}')
    expect(onSubmit).not.toHaveBeenCalled()
    await userEvent.type(screen.getByRole('textbox', { name: 'Motif' }), 'ok{Enter}')
    expect(onSubmit).toHaveBeenCalledWith('ok')
  })
})
