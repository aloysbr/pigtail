import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SessionLaunch, Subject } from '@/api/types'
import { getUi, openPanel, setUi } from '@/state/ui'
import { makeBacklog, withCaptures, type BacklogData } from '@/test/backlog'
import { groupButton, renderApp, type Reply } from '@/test/render'
import { resetToasts } from '@/ui/toasts'
import { canLaunch, LAUNCH_COPY, LAUNCH_POLL_MS, LAUNCH_TIMEOUT_MS, resetLaunches } from './launch'

afterEach(() => {
  vi.useRealTimers()
  resetToasts()
  resetLaunches()
})

/** The daemon's tab name, deliberately not the subject's short title: the toast must print the daemon's. */
const TAB = 'Seedbed · watering (onglet)'

function launchOf(subject: string, extra: Partial<SessionLaunch> = {}): SessionLaunch {
  return {
    launch_id: 'ln_1',
    subject_id: subject,
    state: 'pending',
    cause: '',
    origin: 'atlas',
    workspace_id: '',
    workspace_ref: '',
    cell: 'w1',
    permission_mode: 'default',
    cwd: '',
    tab_title: TAB,
    started_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    deduplicated: false,
    ...extra,
  }
}

/** POST /subjects/{id}/launch answers 202 with a pending launch, which GET /launches/{id} then reads (tab_title "" there). */
async function setup(data: BacklogData = makeBacklog(), id = 'watering-1', post: Partial<SessionLaunch> = {}, status = 202) {
  const h = await renderApp(data)
  h.setRoute(`POST /subjects/${id}/launch`, (): Reply => {
    const l = launchOf(id, post)
    h.data().launches = { ...h.data().launches, [l.launch_id]: { ...l, tab_title: '' } }
    return { status, body: { data: l } }
  })
  return h
}

function refuse(h: Awaited<ReturnType<typeof renderApp>>, id: string, status: number, code?: string, details?: unknown) {
  h.setRoute(`POST /subjects/${id}/launch`, () => ({ status, body: { error: { code: code ?? '', message: 'refused (english)', details } } }))
}

function setLaunch(h: { data(): BacklogData }, patch: Partial<SessionLaunch>) {
  const cur = h.data().launches?.ln_1
  if (!cur) throw new Error('no launch to update')
  h.data().launches = { ln_1: { ...cur, ...patch } }
}

async function pressL(target: Element = document.body) {
  await act(async () => {
    fireEvent.keyDown(target, { key: 'l', code: 'KeyL' })
  })
}

function toastText(): string {
  const t = screen.getAllByRole('status').filter((x) => x.classList.contains('toast6'))
  return t.map((x) => x.textContent ?? '').join(' | ')
}

function panel(): HTMLElement {
  const p = document.querySelector<HTMLElement>('.lpanel')
  if (!p) throw new Error('no subject panel')
  return p
}

function launchButton(scope: HTMLElement): HTMLButtonElement | null {
  return scope.querySelector<HTMLButtonElement>('button.launch-btn')
}

const launchPosts = (h: { writes(): { method: string; url: string }[] }) => h.writes().filter((w) => w.method === 'POST' && w.url.endsWith('/launch'))
const peerReads = (f: { mock: { calls: unknown[][] } }) => f.mock.calls.filter(([u, init]) => String(u) === '/peers' && !(init as RequestInit | undefined)?.method).length

function fake() {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'], shouldAdvanceTime: true })
}

describe('canLaunch', () => {
  const s = { id: 'x', theme_id: '', title: 'x', status: 'inbox', scope: 'perso', updated_at: '', created_at: '' } as Subject
  it('Ouvert and Standby only', () => {
    expect(canLaunch(s, 'ouvert')).toBe(true)
    expect(canLaunch(s, 'standby')).toBe(true)
    expect(canLaunch(s, 'cours')).toBe(false)
    expect(canLaunch(s, 'done')).toBe(false)
    expect(canLaunch(s, 'dropped')).toBe(false)
  })
})

describe('Lancer', () => {
  it('l on an Ouvert: one POST …/launch, « Lancement… » greyed, a second l does nothing; the toast prints the exact prompt', async () => {
    const h = await setup()
    act(() => openPanel('watering-1'))
    await pressL()
    await waitFor(() => expect(launchPosts(h)).toHaveLength(1))
    expect(launchPosts(h)[0]).toEqual({ method: 'POST', url: '/subjects/watering-1/launch', body: undefined })
    const b = launchButton(panel())
    expect(b).toHaveTextContent('Lancement…')
    expect(b).toBeDisabled()
    expect(b).toHaveAttribute('aria-busy', 'true')
    await waitFor(() => expect(toastText()).toContain("Ouverture d'une session dans la cellule w1"))
    await pressL()
    await act(async () => b?.click())
    expect(launchPosts(h)).toHaveLength(1)
  })

  it('launched: « Session ouverte dans cmux » with the cell and the daemon’s tab name, /peers re-read, the button back', async () => {
    fake()
    const h = await setup()
    act(() => openPanel('watering-1'))
    await act(async () => launchButton(panel())?.click())
    await waitFor(() => expect(toastText()).toContain("Ouverture d'une session dans la"))
    const before = peerReads(h.fetchFn)
    setLaunch(h, { state: 'launched', workspace_ref: 'workspace:7' })
    await act(() => vi.advanceTimersByTimeAsync(LAUNCH_POLL_MS))
    await waitFor(() => expect(toastText()).toContain(`Session ouverte dans cmux · cellule w1 · onglet « ${TAB} »`))
    await waitFor(() => expect(peerReads(h.fetchFn)).toBeGreaterThan(before))
    expect(launchButton(panel())).toHaveTextContent('Lancer')
    expect(launchButton(panel())).not.toBeDisabled()
  })

  it('launched with no tab name: the « · onglet … » part is left out', async () => {
    fake()
    const h = await setup(makeBacklog(), 'watering-1', { tab_title: '' })
    act(() => openPanel('watering-1'))
    await pressL()
    setLaunch(h, { state: 'launched' })
    await act(() => vi.advanceTimersByTimeAsync(LAUNCH_POLL_MS))
    await waitFor(() => expect(toastText()).toContain('Session ouverte dans cmux · cellule w1'))
    expect(toastText()).not.toContain('onglet')
  })

  it('200 deduplicated: the same follow-up', async () => {
    fake()
    const h = await setup(makeBacklog(), 'watering-1', { deduplicated: true }, 200)
    act(() => openPanel('watering-1'))
    await pressL()
    await waitFor(() => expect(toastText()).toContain("Ouverture d'une session dans la"))
    setLaunch(h, { state: 'launched' })
    await act(() => vi.advanceTimersByTimeAsync(LAUNCH_POLL_MS))
    await waitFor(() => expect(toastText()).toContain('Session ouverte dans cmux'))
  })

  it('failed: the cause read at the follow-up', async () => {
    fake()
    const h = await setup()
    act(() => openPanel('watering-1'))
    await pressL()
    await waitFor(() => expect(launchPosts(h)).toHaveLength(1))
    setLaunch(h, { state: 'failed', cause: 'cmux new-workspace a échoué' })
    await act(() => vi.advanceTimersByTimeAsync(LAUNCH_POLL_MS))
    await waitFor(() => expect(toastText()).toContain('Le lancement de « Watering atlas » a échoué : cmux new-workspace a échoué.'))
    expect(launchButton(panel())).toHaveTextContent('Lancer')
  })

  it('60 s with no outcome: « Pas de réponse : vérifie cmux », the button back', async () => {
    fake()
    const h = await setup()
    act(() => openPanel('watering-1'))
    await pressL()
    await waitFor(() => expect(launchPosts(h)).toHaveLength(1))
    await act(() => vi.advanceTimersByTimeAsync(LAUNCH_TIMEOUT_MS - LAUNCH_POLL_MS))
    expect(launchButton(panel())).toHaveTextContent('Lancement…')
    await act(() => vi.advanceTimersByTimeAsync(2 * LAUNCH_POLL_MS))
    await waitFor(() => expect(toastText()).toContain('Pas de réponse : vérifie cmux'))
    expect(launchButton(panel())).toHaveTextContent('Lancer')
    expect(launchButton(panel())).not.toBeDisabled()
  })

  it('a POST that never answers: 60 s after the press, « Pas de réponse : vérifie cmux », the button back', async () => {
    fake()
    const h = await renderApp(makeBacklog())
    h.setRoute('POST /subjects/watering-1/launch', () => new Promise<Reply>(() => {}))
    act(() => openPanel('watering-1'))
    await pressL()
    await waitFor(() => expect(launchPosts(h)).toHaveLength(1))
    expect(launchButton(panel())).toHaveTextContent('Lancement…')
    await act(() => vi.advanceTimersByTimeAsync(LAUNCH_TIMEOUT_MS - 1_000))
    expect(launchButton(panel())).toHaveTextContent('Lancement…')
    await act(() => vi.advanceTimersByTimeAsync(1_000))
    await waitFor(() => expect(toastText()).toContain('Pas de réponse : vérifie cmux'))
    expect(launchButton(panel())).toHaveTextContent('Lancer')
    expect(launchButton(panel())).not.toBeDisabled()
  })

  const refusals: [string, number, string | undefined, unknown, string][] = [
    ['SESSION_LIVE', 409, 'SESSION_LIVE', { cell: 'w1' }, 'Une session est déjà ouverte pour « Watering atlas » (cellule w1).'],
    ['LAUNCH_IN_PROGRESS', 409, 'LAUNCH_IN_PROGRESS', undefined, 'Un lancement est déjà en cours pour « Watering atlas ».'],
    ['429', 429, 'LAUNCH_BURST', undefined, "Trop de lancements d'affilée : réessaie dans un instant."],
    ['NO_CELL', 409, 'NO_CELL', undefined, 'Aucune cellule configurée pour le scope perso : ouvre la session à la main.'],
    ['403', 403, undefined, undefined, "Pigtail n'a pas le droit de faire ce changement sur « Watering atlas »."],
    ['503', 503, undefined, undefined, 'Le lancement de « Watering atlas » est indisponible pour l\'instant : réessaie dans un moment.'],
    ['503 CMUX_UNREACHABLE', 503, 'CMUX_UNREACHABLE', undefined, "cmux ne répond pas : la session n'a pas été ouverte."],
    ['LAUNCH_OVER_MARGIN', 409, 'LAUNCH_OVER_MARGIN', undefined, 'Marge du compte dépassée : lancement reporté.'],
    ['423 MAINTENANCE_FROZEN', 423, 'MAINTENANCE_FROZEN', undefined, 'Maintenance en cours : réessaie quand elle est finie.'],
  ]
  for (const [name, status, code, details, copy] of refusals) {
    it(`refused ${name}: its toast, the button back, one call`, async () => {
      const h = await setup()
      refuse(h, 'watering-1', status, code, details)
      act(() => openPanel('watering-1'))
      await pressL()
      await waitFor(() => expect(toastText()).toContain(copy))
      expect(launchPosts(h)).toHaveLength(1)
      expect(launchButton(panel())).toHaveTextContent('Lancer')
      expect(launchButton(panel())).not.toBeDisabled()
    })
  }

  it('En cours: neither Lancer nor Rejoindre (row and sheet); l says where it runs, no call', async () => {
    const data = makeBacklog()
    data.peers[0].cell_id = 'w1'
    const h = await setup(data, 'pig-live')
    await h.user.click(groupButton('th:th_pig'))
    const li = document.querySelector<HTMLElement>('li[data-id="pig-live"]') as HTMLElement
    expect(launchButton(li)).toBeNull()
    expect(within(li).queryByRole('button', { name: /Rejoindre/ })).toBeNull()
    act(() => openPanel('pig-live'))
    expect(launchButton(panel())).toBeNull()
    expect(within(panel()).queryByRole('button', { name: /Rejoindre|Lancer/ })).toBeNull()
    await pressL()
    await waitFor(() => expect(toastText()).toContain('Déjà en cours dans la cellule w1.'))
    expect(launchPosts(h)).toHaveLength(0)
  })

  it('opening copy shows no hard-coded prompt', () => {
    const t = LAUNCH_COPY.opening('default')
    expect(t).not.toContain('/example-skill')
    expect(t).not.toContain('sujet')
  })

  it('Done and Abandonné: no button, l does nothing', async () => {
    const h = await setup(makeBacklog(), 'pig-d1')
    act(() => openPanel('pig-d1'))
    expect(launchButton(panel())).toBeNull()
    await pressL()
    act(() => openPanel('pig-x1'))
    expect(launchButton(panel())).toBeNull()
    await pressL()
    expect(launchPosts(h)).toHaveLength(0)
  })

  it('a globex subject launches in its scope’s cell, no screen error', async () => {
    const h = await setup(makeBacklog(), 'wk-1', { cell: 'work' })
    act(() => openPanel('wk-1'))
    await act(async () => launchButton(panel())?.click())
    await waitFor(() => expect(launchPosts(h)).toEqual([{ method: 'POST', url: '/subjects/wk-1/launch', body: undefined }]))
    await waitFor(() => expect(toastText()).toContain("Ouverture d'une session dans la cellule work"))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('l on a selected capture: no call', async () => {
    const h = await setup(withCaptures())
    act(() => setUi({ sel: 'cap-b1' }))
    await pressL()
    expect(getUi().sel).toBe('cap-b1')
    expect(h.writes()).toEqual([])
  })

  it('the row carries « Lancer l » on Ouvert and Standby; a click launches without opening the sheet', async () => {
    const h = await setup(makeBacklog(), 'pig-r2')
    await h.user.click(groupButton('th:th_pig'))
    await h.user.click(groupButton('th:th_pig:standby'))
    const r2 = document.querySelector<HTMLElement>('li[data-id="pig-r2"]') as HTMLElement
    const p1 = document.querySelector<HTMLElement>('li[data-id="pig-p1"]') as HTMLElement
    expect(within(r2).getByRole('button', { name: 'Lancer' })).toHaveClass('launch-btn')
    expect(within(p1).getByRole('button', { name: 'Lancer' })).toBeInTheDocument()
    await h.user.click(within(r2).getByRole('button', { name: 'Lancer' }))
    await waitFor(() => expect(launchPosts(h)).toHaveLength(1))
    expect(getUi().panel).toBe(false)
    expect(getUi().sel).toBe('pig-r2')
    expect(within(r2).getByRole('button', { name: 'Lancement…' })).toHaveAttribute('aria-busy', 'true')
  })
})

describe('Lancer on the phone (390 px): the sheet only, as t10', () => {
  afterEach(() => resetLaunches())

  it('no Lancer on the selected row; the sheet keeps its full-width button; l still launches', async () => {
    const h = await renderApp(makeBacklog(), { width: 390 })
    h.setRoute('POST /subjects/pig-r1/launch', () => ({ status: 202, body: { data: launchOf('pig-r1') } }))
    await h.user.click(groupButton('th:th_pig'))
    act(() => setUi({ sel: 'pig-r1' }))
    const li = document.querySelector<HTMLElement>('li[data-id="pig-r1"]') as HTMLElement
    expect(li).toHaveClass('sel')
    expect(launchButton(li)).toBeNull()
    act(() => openPanel('pig-r1'))
    expect(launchButton(panel())).toHaveTextContent('Lancer')
    await pressL()
    await waitFor(() => expect(launchPosts(h)).toHaveLength(1))
  })
})

describe('touch targets (jsdom measures no height: the rules are read)', () => {
  const css = (f: string) => readFileSync(join(process.cwd(), 'src', f), 'utf8')
  /** The body of the first `@container (max-width: 640px)` block of a stylesheet. */
  function narrowBlock(src: string): string {
    const i = src.indexOf('@container (max-width: 640px)')
    if (i < 0) return ''
    let depth = 0
    for (let j = src.indexOf('{', i); j < src.length; j += 1) {
      if (src[j] === '{') depth += 1
      if (src[j] === '}') depth -= 1
      if (depth === 0) return src.slice(i, j + 1)
    }
    return ''
  }

  it('.launch-btn is a touch target on the phone only: a narrow desktop list keeps its row height (j/k does not jump)', () => {
    const src = css('list/list.css')
    expect(narrowBlock(src)).not.toMatch(/\.launch-btn\s*\{[^}]*min-height/)
    expect(src).toMatch(/\.phone \.launch-btn\s*\{[^}]*min-height:\s*var\(--size-touch\)/)
    expect(src).not.toMatch(/44px/)
  })

  it('a narrow row lays its Lancer over rank and age: no extra column, so ranks and ages stay aligned', () => {
    const block = narrowBlock(css('list/list.css'))
    expect(block).toMatch(/\.lr6, \.no-theme \.lr6 \{ grid-template-columns: var\(--size-icon\) minmax\(0, 1fr\) var\(--size-col-rank\) var\(--size-col-age\);[^}]*\}/)
    expect(block).toMatch(/\.lr6 \.lr-l \{[^}]*position: absolute/)
    expect(css('list/list.css')).toMatch(/\.lr6 \{[^}]*position: relative/)
  })

  it('the phone sheet’s Lancer is full width and primary (t10 mbtn primary)', () => {
    expect(css('list/list.css')).toMatch(/\.phone \.lp-acts \.launch-btn \{[^}]*width: 100%[^}]*background: var\(--btn-fill\)/)
  })

  it('the toast’s « annuler » is a touch target on the phone', () => {
    const src = css('ui/toast.css')
    expect(src).toMatch(/\.phone \.toast-undo\s*\{[^}]*min-height:\s*var\(--size-touch\)/)
    expect(src).not.toMatch(/44px/)
  })
})
