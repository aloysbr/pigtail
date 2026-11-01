import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { WaitCard } from '@/ui/WaitCard'
import { ApiProvider, makeClient } from './client'
import { makeSend, SendProvider } from './mutate'
import { makeQueryClient } from './queries'
import type { Wait } from './types'
import { answerText, subjectWaitsRoute, sessionWaitsRoute, useSubjectWaits, WAITS_LIVE_MS } from './waits'

const NOW = Date.parse('2026-10-27T09:40:00+01:00')

function open(extra: Partial<Wait> = {}): Wait {
  return {
    id: 'wait_1',
    session_id: 'sess-1',
    subject_id: 'pig-r1',
    type: 'arbitrage',
    source: 'declared',
    state: 'open',
    reason: 'Calque : plein écran ou fenêtre ?',
    opened_at: '2026-10-27T09:12:00+01:00',
    details: { options: [{ id: 'A', label: 'Plein écran' }, { id: 'B', label: 'Fenêtre', reco: true }] },
    ...extra,
  }
}

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

/** The subject's waits as the dock reads them, each an answer card. */
function Waits({ subjectId }: { subjectId: string }) {
  const q = useSubjectWaits(subjectId, { closed: 2 }, false)
  return (
    <>
      {(q.data ?? []).map((w) => (
        <WaitCard key={w.id} variant="answer" wait={w} now={NOW} />
      ))}
    </>
  )
}

describe('waits routes', () => {
  it('GET /waits?subject=…&closed=2 and ?session=…&closed=2', () => {
    expect(subjectWaitsRoute('pig r1', 2)).toBe('/waits?subject=pig%20r1&closed=2')
    expect(sessionWaitsRoute('sess-1', 2)).toBe('/waits?session=sess-1&closed=2')
    expect(WAITS_LIVE_MS).toBe(3_000)
  })

  it('answerText: the option and its label, the text, « Vu »', () => {
    const w = open()
    expect(answerText(w, { choice: 'B', reason: 'note' })).toBe('B · Fenêtre — note')
    expect(answerText(w, { text: 'libre' })).toBe('libre')
    expect(answerText(w, { seen: true })).toBe('Vu')
  })
})

describe('useAnswerWait: optimistic (R19), then a refusal', () => {
  it('shows « Réponse envoyée » at once, field gone; a 4xx puts the text and the choice back with the reason', async () => {
    const post = deferred<Response>()
    const fetchFn = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      if ((init?.method ?? 'GET') === 'POST') return post.promise
      expect(String(url)).toBe('/waits?subject=pig-r1&closed=2')
      return new Response(JSON.stringify({ data: [open()] }), { status: 200 })
    })
    const qc = makeQueryClient()
    qc.setDefaultOptions({ queries: { retry: false, refetchInterval: false } })
    const user = userEvent.setup()
    render(
      <ApiProvider value={makeClient({ fetch: fetchFn as unknown as typeof fetch, reload: vi.fn(), storage: null })}>
        <SendProvider value={makeSend({ fetch: fetchFn as unknown as typeof fetch })}>
          <QueryClientProvider client={qc}>
            <Waits subjectId="pig-r1" />
          </QueryClientProvider>
        </SendProvider>
      </ApiProvider>,
    )
    const field = await screen.findByRole('textbox', { name: 'Ta réponse' })
    await user.click(screen.getByRole('radio', { name: /^A · Plein écran/ }))
    await user.type(field, 'garde le menu')
    await user.click(screen.getByRole('button', { name: /Envoyer/ }))
    // optimistic: before the answer, the wait reads as answered and the field is gone (never greyed)
    await waitFor(() => expect(screen.getByRole('region', { name: 'Réponse envoyée' })).toHaveTextContent('Réponse envoyée · la session reprend'))
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.getByRole('region', { name: 'Réponse envoyée' })).toHaveTextContent('A · Plein écran — garde le menu')
    // the refusal re-reads the waits: the stub answers the wait still open
    await act(async () => {
      post.resolve(new Response(JSON.stringify({ error: { code: 'CONFLICT', message: 'wait wait_1 is answered' } }), { status: 409 }))
    })
    const back = await screen.findByRole('textbox', { name: 'Ta réponse' })
    expect(back).toHaveValue('garde le menu')
    expect(screen.getByRole('radio', { name: /^A · Plein écran/ })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('alert')).toHaveTextContent(
      "Ta réponse n'est pas partie : l'attente a changé entre-temps (déjà répondue ou fermée). Elle est restée dans le champ.",
    )
  })
})
