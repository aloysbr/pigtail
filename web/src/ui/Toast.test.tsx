import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { openPanel } from '@/state/ui'
import { renderApp } from '@/test/render'
import { ToastHost } from './Toast'
import { pushToast, resetToasts } from './toasts'

describe('ToastHost', () => {
  afterEach(() => resetToasts())

  it('the text is read by a screen reader (role status)', () => {
    render(<ToastHost />)
    act(() => {
      pushToast({ text: '« Atlas web » en standby' })
    })
    expect(screen.getByRole('status')).toHaveTextContent('« Atlas web » en standby')
  })

  it('an undoable toast has an « annuler » button that undoes it', async () => {
    const run = vi.fn(async () => {})
    render(<ToastHost />)
    act(() => {
      pushToast({ text: 'Rang de « A » modifié', undo: { title: 'A', label: 'Rang de « A » rétabli', run, stillValid: () => true } })
    })
    const b = screen.getByRole('button', { name: 'annuler' })
    await act(async () => {
      fireEvent.click(b)
    })
    expect(run).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('status')).toHaveTextContent('Annulé · Rang de « A » rétabli')
  })

  it('a plain toast has no button', () => {
    render(<ToastHost />)
    act(() => {
      pushToast({ text: 'x' })
    })
    expect(screen.queryByRole('button', { name: 'annuler' })).toBeNull()
  })

  it('the web front mounts the host; z (no modifier, outside a field) undoes the last toast', async () => {
    const run = vi.fn(async () => {})
    const { user } = await renderApp()
    act(() => {
      pushToast({ text: 'Rang de « A » modifié', undo: { title: 'A', label: 'Rang de « A » rétabli', run, stillValid: () => true } })
    })
    // typing z in the search does not undo
    await user.click(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }))
    await user.keyboard('z')
    expect(run).not.toHaveBeenCalled()
    ;(document.activeElement as HTMLElement).blur()
    await act(async () => {
      fireEvent.keyDown(document.body, { key: 'z', metaKey: true })
    })
    expect(run).not.toHaveBeenCalled()
    await act(async () => {
      fireEvent.keyDown(document.body, { key: 'z', code: 'KeyW' })
    })
    expect(run).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('status')).toHaveTextContent('Annulé · Rang de « A » rétabli')
  })

  it('placed in the list column, not the window: inside .lmain, which reserves the room under it while it shows (verify V1)', async () => {
    await renderApp(undefined, { width: 1024 })
    const main = document.querySelector('.lmain') as HTMLElement
    expect(main).not.toHaveClass('has-toast')
    act(() => {
      pushToast({ text: 'Rang de « A » modifié' })
    })
    const toast = screen.getByText('Rang de « A » modifié').closest('.toast6')
    expect(main.contains(toast)).toBe(true)
    expect(main).toHaveClass('has-toast')
    expect(document.documentElement.style.getPropertyValue('--toast-h')).toMatch(/px$/)
    act(() => resetToasts())
    expect(main).not.toHaveClass('has-toast')
    expect(document.documentElement.style.getPropertyValue('--toast-h')).toBe('')
  })

  it('a sheet in page mode hides the list: the toast still shows, outside .lmain', async () => {
    await renderApp(undefined, { width: 390 })
    act(() => openPanel('pig-r1'))
    act(() => {
      pushToast({ text: 'x page' })
    })
    const toast = screen.getByText('x page').closest('.toast6')
    expect(toast).not.toBeNull()
    expect(document.querySelector('.lmain')?.contains(toast)).toBe(false)
  })

  it('on the phone the list reserves the toast plus its offset from the foot, so the last row scrolls out from under it', () => {
    const css = readFileSync(join(process.cwd(), 'src/ui/toast.css'), 'utf8')
    expect(css).toMatch(/\.phone \.lmain\.has-toast \.ll6 \{[^}]*padding-bottom: calc\(var\(--toast-h, var\(--size-touch\)\) \+ var\(--sp-6\) \+ env\(safe-area-inset-bottom, 0px\) \+ var\(--sp-8\)\)/)
  })
})
