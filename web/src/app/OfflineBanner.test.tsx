import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderApp, rowIds, unfoldAll } from '@/test/render'
import { OfflineBanner } from './OfflineBanner'

afterEach(() => {
  vi.useRealTimers()
})

describe('Offline banner (G3)', () => {
  it('counts down to the next attempt, second by second', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-25T10:00:00Z'))
    render(<OfflineBanner nextRetryAt={Date.now() + 10_000} fetching={false} />)
    expect(screen.getByRole('status')).toHaveTextContent('pigtaild ne répond pas — nouvel essai dans 10 s')
    act(() => {
      vi.advanceTimersByTime(3_000)
    })
    expect(screen.getByRole('status')).toHaveTextContent('nouvel essai dans 7 s')
    act(() => {
      vi.advanceTimersByTime(7_000)
    })
    expect(screen.getByRole('status')).toHaveTextContent('nouvel essai en cours')
  })

  it('while the re-read runs it says so', () => {
    render(<OfflineBanner nextRetryAt={Date.now() + 5_000} fetching />)
    expect(screen.getByRole('status')).toHaveTextContent('pigtaild ne répond pas — nouvel essai en cours')
  })

  it('has a warning tone: an icon, hidden from the reader (the text says it)', () => {
    render(<OfflineBanner nextRetryAt={Date.now() + 5_000} fetching={false} />)
    const ico = screen.getByRole('status').querySelector('svg.d-banner-ico')
    expect(ico).not.toBeNull()
    expect(ico?.getAttribute('aria-hidden')).toBe('true')
  })

  it('« Réessayer maintenant » calls onRetry; absent without a handler; disabled while a re-read runs', () => {
    const onRetry = vi.fn()
    const { rerender } = render(<OfflineBanner nextRetryAt={Date.now() + 5_000} fetching={false} onRetry={onRetry} />)
    fireEvent.click(screen.getByRole('button', { name: 'Réessayer maintenant' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
    rerender(<OfflineBanner nextRetryAt={Date.now() + 5_000} fetching onRetry={onRetry} />)
    expect(screen.getByRole('button', { name: 'Réessayer maintenant' })).toBeDisabled()
    rerender(<OfflineBanner nextRetryAt={Date.now() + 5_000} fetching={false} />)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('a failed re-read shows the banner in the header and keeps the previous rows; the first success hides it', async () => {
    const c = await renderApp()
    unfoldAll()
    const before = rowIds()
    expect(before.length).toBeGreaterThan(0)
    expect(screen.queryByText(/pigtaild ne répond pas/)).toBeNull()
    c.setFailing(true)
    await act(() => c.refetch())
    const banner = await screen.findByRole('status')
    expect(banner).toHaveTextContent(/pigtaild ne répond pas — nouvel essai (dans \d+ s|en cours)/)
    expect(banner.closest('header.d-head')).not.toBeNull()
    c.setFailing(false)
    fireEvent.click(screen.getByRole('button', { name: 'Réessayer maintenant' }))
    await waitFor(() => expect(screen.queryByText(/pigtaild ne répond pas/)).toBeNull())
    c.setFailing(true)
    await act(() => c.refetch())
    await screen.findByRole('status')
    expect(rowIds()).toEqual(before)
    c.setFailing(false)
    await act(() => c.refetch())
    await waitFor(() => expect(screen.queryByText(/pigtaild ne répond pas/)).toBeNull())
    expect(rowIds()).toEqual(before)
  })
})
