import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { makeSend, SendProvider } from '@/api/mutate'
import { makeQueryClient } from '@/api/queries'
import type { Wait } from '@/api/types'
import { WaitCard } from './WaitCard'

const NOW = Date.parse('2026-10-27T09:40:00+01:00')

describe('WaitCard (read only)', () => {
  it('heads « Attend ta réponse · depuis … » with the type chip, then the reason', () => {
    render(<WaitCard type="arbitrage" reason="Maquette t1 : quel style ?" since="2026-10-27T09:12:00+01:00" now={NOW} />)
    const card = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(card).toHaveClass('wait')
    const head = card.querySelector('.wait-h')!
    expect(head).toHaveTextContent(/^Attend ta réponse/)
    expect(head.querySelector('.muted')?.textContent).toMatch(/^· depuis /)
    expect(head.querySelector('.kind')).toHaveTextContent('Arbitrage')
    expect(card.querySelector('.wait-q')).toHaveTextContent('Maquette t1 : quel style ?')
  })

  it('no button at all: no Go ahead, no Répondre, no option (no answer route in the web front)', () => {
    render(<WaitCard type="question" reason="Quel compte ?" since="2026-10-27T09:20:00+01:00" now={NOW} />)
    const card = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(card.querySelectorAll('button, [role="radio"], input, textarea')).toHaveLength(0)
    expect(card.querySelector('.wait-f')).toBeNull()
    expect(card.textContent).not.toMatch(/Répondre|Go ahead/)
  })

  it('an untyped wait reads « En attente »; no reason, no body; no since, no « depuis »', () => {
    render(<WaitCard type="" since="" now={NOW} />)
    const card = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(card.querySelector('.kind')).toHaveTextContent('En attente')
    expect(card.querySelector('.wait-b')).toBeNull()
    expect(card.textContent).not.toMatch(/depuis/)
  })

  it('children: an action slot under the reason, absent when there are none', () => {
    const { rerender } = render(<WaitCard type="question" reason="Quel compte ?" since="2026-10-27T09:20:00+01:00" now={NOW} />)
    expect(document.querySelector('.wait-f')).toBeNull()
    rerender(
      <WaitCard type="question" reason="Quel compte ?" since="2026-10-27T09:20:00+01:00" now={NOW}>
        <button type="button">Oui</button>
      </WaitCard>,
    )
    const card = screen.getByRole('region', { name: 'Attend ta réponse' })
    const slot = card.querySelector('.wait-f')!
    expect(slot).toContainElement(screen.getByRole('button', { name: 'Oui' }))
    expect(slot.previousElementSibling).toBe(card.querySelector('.wait-b'))
  })
})

/** A wait as GET /waits answers it; open, declared, by default an arbitrage with two options (B the reco). */
function wait(extra: Partial<Wait> = {}): Wait {
  return {
    id: 'wait_1',
    session_id: 'sess-1',
    subject_id: 'pig-r1',
    type: 'arbitrage',
    source: 'declared',
    state: 'open',
    reason: 'Calque : plein écran ou fenêtre ?',
    opened_at: '2026-10-27T09:12:00+01:00',
    details: {
      options: [
        { id: 'A', label: 'Plein écran' },
        { id: 'B', label: 'Fenêtre', reco: true },
      ],
    },
    ...extra,
  }
}

/** The answer variant over a stubbed POST /waits/{id}/answer; returns the bodies it received. */
function renderAnswer(w: Wait, ended?: boolean) {
  const bodies: unknown[] = []
  const fetchFn = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
    bodies.push(typeof init?.body === 'string' ? JSON.parse(init.body) : undefined)
    return new Response(JSON.stringify({ data: { wait: { ...w, state: 'answered' } } }), { status: 200 })
  })
  const qc = makeQueryClient()
  qc.setDefaultOptions({ queries: { retry: false, refetchInterval: false } })
  const user = userEvent.setup()
  render(
    <SendProvider value={makeSend({ fetch: fetchFn as unknown as typeof fetch })}>
      <QueryClientProvider client={qc}>
        <WaitCard variant="answer" wait={w} now={NOW} ended={ended} />
      </QueryClientProvider>
    </SendProvider>,
  )
  return { user, bodies, fetchFn }
}

function Providers({ children }: { children: React.ReactNode }) {
  const qc = makeQueryClient()
  return (
    <SendProvider value={makeSend({ fetch: vi.fn() as unknown as typeof fetch })}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </SendProvider>
  )
}

describe('WaitCard, the answer variant (T12)', () => {
  it('arbitrage: its options as radios, the reco tagged; ⌘↩ sends {choice, reason}', async () => {
    const { user, bodies, fetchFn } = renderAnswer(wait())
    const group = screen.getByRole('radiogroup', { name: 'Options' })
    const radios = within(group).getAllByRole('radio')
    expect(radios).toHaveLength(2)
    expect(radios[1]).toHaveTextContent('B · Fenêtre')
    expect(radios[1]).toHaveTextContent('reco')
    expect(radios[0]).not.toHaveTextContent('reco')
    await user.click(radios[1])
    expect(radios[1]).toHaveAttribute('aria-checked', 'true')
    const field = screen.getByRole('textbox', { name: 'Ta réponse' })
    await user.type(field, 'garde le menu')
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    expect(fetchFn).toHaveBeenCalledTimes(1)
    expect(String(fetchFn.mock.calls[0][0])).toBe('/waits/wait_1/answer')
    expect(bodies[0]).toEqual({ choice: 'B', reason: 'garde le menu' })
    expect(screen.getByRole('button', { name: /Envoyer/ })).toHaveTextContent('Envoyer ⌘↩')
  })

  it('answered card: « la session reprend » while the session lives, the dock’s ended wording once it ended', () => {
    const answered = wait({ state: 'answered', answer: { at: '2026-10-27T09:30:00+01:00', text: 'ok' } as Wait['answer'] })
    const { unmount } = render(<Providers><WaitCard variant="answer" wait={answered} now={NOW} /></Providers>)
    expect(screen.getByRole('region', { name: 'Réponse envoyée' })).toHaveTextContent('Réponse envoyée · la session reprend')
    unmount()
    render(<Providers><WaitCard variant="answer" wait={answered} now={NOW} ended /></Providers>)
    const card = screen.getByRole('region', { name: 'Réponse envoyée' })
    expect(card).toHaveTextContent('Réponse envoyée · session terminée, ta réponse ira à sa prochaine session')
    expect(card).not.toHaveTextContent('la session reprend')
  })

  it('question without options: no radio, ⌘↩ sends {text}', async () => {
    const { user, bodies } = renderAnswer(wait({ type: 'question', reason: 'Quel compte ?', details: {} }))
    expect(screen.queryByRole('radio')).toBeNull()
    await user.type(screen.getByRole('textbox', { name: 'Ta réponse' }), 'le perso')
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    expect(bodies).toEqual([{ text: 'le perso' }])
  })

  it('permission: what is asked, no field, no option, no send (absent, never greyed)', () => {
    renderAnswer(wait({ type: 'permission', source: 'auto', reason: 'Bash', details: { cmd: 'rm -rf build' } }))
    const card = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(card).toHaveTextContent('rm -rf build')
    expect(card.querySelectorAll('button, [role="radio"], textarea')).toHaveLength(0)
  })

  it('an auto wait of type arbitrage (a native dialog): neither options nor field', () => {
    renderAnswer(wait({ source: 'auto' }))
    const card = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(card.querySelectorAll('button, [role="radio"], textarea')).toHaveLength(0)
  })

  it('review with items (D9): the items, no field', () => {
    renderAnswer(wait({ type: 'review', reason: 'Acceptance t2', details: { items: [{ label: 'Le lecteur' }, { label: 'Le panneau' }] } }))
    const card = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(card).toHaveTextContent('Le lecteur')
    expect(card).toHaveTextContent('Le panneau')
    expect(card.querySelectorAll('button, [role="radio"], textarea')).toHaveLength(0)
  })

  it('review without items (D9): « Vu » sends {seen}, a text and ⌘↩ sends {text}', async () => {
    const { user, bodies } = renderAnswer(wait({ type: 'review', reason: 'Spec à relire', details: {} }))
    expect(screen.queryByRole('radio')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Vu' }))
    expect(bodies).toEqual([{ seen: true }])
  })

  it('review without items: a text and ⌘↩ sends {text}', async () => {
    const { user, bodies } = renderAnswer(wait({ type: 'review', reason: 'Spec à relire', details: {} }))
    await user.type(screen.getByRole('textbox', { name: 'Ta réponse' }), 'ok sauf § 3')
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    expect(bodies).toEqual([{ text: 'ok sauf § 3' }])
  })
})
