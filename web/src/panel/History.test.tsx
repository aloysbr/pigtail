import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiProvider, type Get } from '@/api/client'
import type { LogEntry } from '@/api/types'
import { makeBacklog } from '@/test/backlog'
import { JOURNAL_FIXTURE, JOURNAL_NOW, JOURNAL_TZ } from '@/test/journal-fixture'
import { groupButton, renderApp } from '@/test/render'
import { blocks } from '@/theme/css-parse'
import { History, HISTORY_CAP } from './History'

// the fixture reads the t4 times in Paris; the sheet prints local time
process.env.TZ = JOURNAL_TZ

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'], shouldAdvanceTime: true })
  vi.setSystemTime(JOURNAL_NOW)
})
afterEach(() => {
  vi.useRealTimers()
})

/** Renders the section alone over a log answer (or a failing read). */
function renderHistory(log: LogEntry[] | Error | 'pending') {
  const get = (async () => {
    if (log === 'pending') return new Promise(() => {})
    if (log instanceof Error) throw log
    return log
  }) as unknown as Get
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <ApiProvider value={get}>
      <QueryClientProvider client={qc}>
        <History id="sub_demo" />
      </QueryClientProvider>
    </ApiProvider>,
  )
  return { user: userEvent.setup({ advanceTimers: vi.advanceTimersByTime }), sec: () => screen.getByRole('heading', { name: 'Historique' }).closest('section') as HTMLElement }
}

/** The shown rows (not the unfolded ones): type, text, time. */
function rowTexts(sec: HTMLElement): string[][] {
  return [...sec.querySelectorAll('ol.hv-evs > li.hv-ev')].map((li) => [
    li.querySelector(':scope > .hv-ty')?.textContent ?? '',
    li.querySelector(':scope > .hv-tx')?.textContent ?? '',
    li.querySelector(':scope > .hv-t')?.textContent ?? '',
  ])
}

const CSS = readFileSync(join(process.cwd(), 'src/panel/panel.css'), 'utf8')

describe('Historique by day, step and event (t4)', () => {
  it('the day header, then the steps newest first with their span, « en cours » on write', async () => {
    const { user, sec } = renderHistory(JOURNAL_FIXTURE)
    expect(await screen.findByRole('heading', { name: "Aujourd'hui · lundi 26 octobre · 15 événements" })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Voir tout' }))
    const steps = [...sec().querySelectorAll('.hv-st')].map((h) => h.textContent)
    expect(steps).toEqual([
      'writeen cours15:37 – 15:38',
      'sketcht1 → t314:45 – 15:32',
      'pick14:11 – 14:13',
      'survey14:02',
      'Sans étape13:55',
    ])
    expect(sec().querySelector('.hv-step.cur .hv-sn')?.textContent).toBe('write')
    expect(sec().querySelectorAll('.hv-cur')).toHaveLength(1)
  })

  it('each event: type in full, text, time on the right, detail below, round chip hanging before the text', async () => {
    const { user, sec } = renderHistory(JOURNAL_FIXTURE)
    await user.click(await screen.findByRole('button', { name: 'Voir tout' }))
    const rows = rowTexts(sec())
    expect(rows).toHaveLength(11)
    expect(rows[0]).toEqual(['Attente', 'périmètre face aux manques du capteur et réglage par défaut en attente', '15:38'])
    expect(rows.map((r) => r[0])).toEqual([
      'Attente',
      'Sortie',
      'Sortie',
      'Décision',
      'Attente',
      'Attente',
      'Sortie',
      'Idée',
      'Décision',
      'Sortie',
      'Session',
    ])
    const first = sec().querySelector('ol.hv-evs > li.hv-ev') as HTMLElement
    expect(first.querySelector('svg.hv-ic')).not.toBeNull()
    expect(first.querySelector('.hv-d')?.textContent).toBe('Fin de session · brouillon écrit depuis t3')
    const handoff = sec().querySelector('li[data-id="log_bf66764b3a71f2c5"]') as HTMLElement
    expect(handoff.querySelector(':scope > .hv-tx.hv-tx-r > .hv-r')?.textContent).toBe('t3')
    expect(handoff.querySelector(':scope > .hv-tx .hv-m')?.textContent).toBe('Handoff — Pilote pluviomètre (2026-10-26)')
    // a step with a single round shows no chip
    expect(sec().querySelector('li[data-id="log_59b50005854955b9"] .hv-r')).toBeNull()
  })

  it('the folds read « 1 de plus · session » and « 2 de plus · attente, statut » and unfold on a click', async () => {
    const { user, sec } = renderHistory(JOURNAL_FIXTURE)
    await user.click(await screen.findByRole('button', { name: 'Voir tout' }))
    const one = screen.getByRole('button', { name: '1 de plus · session' })
    const two = screen.getByRole('button', { name: '2 de plus · attente, statut' })
    expect(screen.getByRole('button', { name: '1 de plus · statut' })).toBeInTheDocument()
    expect(one).toHaveAttribute('aria-expanded', 'false')
    expect(sec().querySelector('li[data-id="log_d94949640ed7344e"]')).toBeNull()
    await user.click(two)
    expect(two).toHaveAttribute('aria-expanded', 'true')
    const unfolded = sec().querySelector('li[data-id="log_171b6b525895a18d"] > ol.hv-fold') as HTMLElement
    expect([...unfolded.querySelectorAll(':scope > li > .hv-ty')].map((x) => x.textContent)).toEqual(['Attente', 'Statut'])
    expect(within(unfolded).getByText('understanding → designing')).toBeInTheDocument()
    await user.click(two)
    expect(sec().querySelector('li[data-id="log_171b6b525895a18d"] > ol.hv-fold')).toBeNull()
  })

  it(`« Voir tout » past ${HISTORY_CAP} rows; the counts stay whole`, async () => {
    const { user, sec } = renderHistory(JOURNAL_FIXTURE)
    await screen.findByRole('heading', { name: "Aujourd'hui · lundi 26 octobre · 15 événements" })
    expect(rowTexts(sec())).toHaveLength(HISTORY_CAP)
    await user.click(screen.getByRole('button', { name: 'Voir tout' }))
    expect(rowTexts(sec())).toHaveLength(11)
    expect(screen.queryByRole('button', { name: 'Voir tout' })).toBeNull()
  })

  it('no « Voir tout » with 10 rows or fewer', async () => {
    renderHistory(JOURNAL_FIXTURE.slice(0, 6))
    await screen.findByRole('heading', { name: /^Aujourd'hui/ })
    expect(screen.queryByRole('button', { name: 'Voir tout' })).toBeNull()
  })

  it('loading, error (C8) and empty (C8) states', async () => {
    renderHistory('pending')
    expect(screen.getByText('Chargement…')).toBeInTheDocument()
  })

  it("a failed read says « Historique indisponible pour l'instant. »", async () => {
    renderHistory(new Error('down'))
    expect(await screen.findByText("Historique indisponible pour l'instant.")).toBeInTheDocument()
  })

  it("an empty journal says « Aucune entrée dans l'historique. »", async () => {
    const { sec } = renderHistory([])
    expect(await screen.findByText("Aucune entrée dans l'historique.")).toBeInTheDocument()
    expect(sec().querySelector('.hv-evs')).toBeNull()
  })

  it('in the web front: the sheet of a subject shows its journal by step', async () => {
    const data = makeBacklog()
    data.logs = { 'pig-r1': JOURNAL_FIXTURE }
    const r = await renderApp(data)
    await r.user.click(groupButton('th:th_pig'))
    await r.user.click(document.querySelector('li[data-id="pig-r1"]') as HTMLElement)
    expect(await screen.findByRole('heading', { name: "Aujourd'hui · lundi 26 octobre · 15 événements" })).toBeInTheDocument()
  })
})

describe('Historique styles (panel.css)', () => {
  const hv = blocks(CSS).filter((b) => /\.hv-/.test(b.selector))

  it('reads the history rules (positive control)', () => {
    expect(hv.length).toBeGreaterThan(5)
  })

  it('the type and time columns read --size-hv-type and --size-hv-time', () => {
    const ev = hv.find((b) => /\.hv-ev\b/.test(b.selector) && /grid-template-columns/.test(b.body))
    expect(ev?.body).toMatch(/var\(--size-hv-type\)/)
    expect(ev?.body).toMatch(/var\(--size-hv-time\)/)
  })

  it('no --accent-attn-* in the history rules: nothing there awaits an answer', () => {
    expect(hv.filter((b) => /--accent-attn-/.test(b.body)).map((b) => b.selector)).toEqual([])
  })
})
