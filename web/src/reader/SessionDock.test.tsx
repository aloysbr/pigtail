import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { getUi } from '@/state/ui'
import { makeBacklog, minutesAgo } from '@/test/backlog'
import { arb, group, openDock, past, peer } from '@/test/dock'
import { groupButton, renderApp, setWidth } from '@/test/render'
import { blocks } from '@/theme/css-parse'
import { resetToasts } from '@/ui/toasts'

afterEach(() => {
  vi.useRealTimers()
  resetToasts()
})

describe('SessionDock (D5–D8): the session of the open deliverable, over the reader', () => {
  it('is mounted in the reader, kept live (data-session-dock), never inert', async () => {
    const { dock } = await openDock((d) => {
      d.waits = [arb()]
    })
    expect(dock.closest('[inert]')).toBeNull()
  })

  it('picks the session that waits first, over a more recent live one of the subject', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle', updated_at: minutesAgo(30), cell_id: 'w1' }), peer('sess-new', { cell_id: 'n2', updated_at: minutesAgo(0) })]
      d.waits = [arb()]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(panel).toHaveTextContent('Atlas web')
    expect(panel).toHaveTextContent('Calque : plein écran ou fenêtre ?')
  })

  it('without a wait: the most recent live session of the subject', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-old', { cell_id: 'o1', cmux_title: 'Vieille', updated_at: minutesAgo(40) }), peer('sess-new', { cell_id: 'n2', cmux_title: 'Neuve n2', updated_at: minutesAgo(1) })]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(panel.querySelector('.sd-id span')).toHaveTextContent(/^Travaille · Vivante · Neuve n2$/)
  })

  it('arbitrage: its options with the reco, the field and « Envoyer ⌘↩ »; the last exchange above the wait', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle' })]
      d.waits = [arb(), past('wait_p1', 'sess-w', 20, 'Quel nom ?', 'outputs-consultables'), past('wait_p0', 'sess-w', 60, 'Premier ?', 'oui')]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(within(panel).getAllByRole('radio')).toHaveLength(2)
    expect(within(panel).getByRole('radio', { name: /^A · Plein écran/ })).toHaveTextContent('reco')
    expect(within(panel).getByRole('textbox', { name: 'Ta réponse' })).toBeInTheDocument()
    expect(within(panel).getByRole('button', { name: /Envoyer/ })).toHaveTextContent('Envoyer ⌘↩')
    const xs = within(panel).getByRole('list', { name: 'Échanges récents' })
    // one exchange: the latest
    expect(within(xs).getAllByRole('listitem')).toHaveLength(1)
    expect(xs).toHaveTextContent('Quel nom ?')
    expect(xs).toHaveTextContent('Toi')
    expect(xs).toHaveTextContent('outputs-consultables')
    // above the wait card
    const card = within(panel).getByRole('region', { name: 'Attend ta réponse' })
    expect(xs.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('the session works: no field, « La session travaille » and its two last exchanges; folded, « Travaille »', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [peer('sess-w')]
      d.waits = [past('wait_p2', 'sess-w', 10, 'Trois ?', 'c'), past('wait_p1', 'sess-w', 20, 'Deux ?', 'b'), past('wait_p0', 'sess-w', 60, 'Un ?', 'a')]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    await waitFor(() => expect(panel).toHaveTextContent('La session travaille'))
    expect(within(panel).queryByRole('textbox')).toBeNull()
    const items = within(within(panel).getByRole('list', { name: 'Échanges récents' })).getAllByRole('listitem')
    expect(items.map((li) => li.querySelector('.sd-xq')?.textContent)).toEqual(['Deux ?', 'Trois ?'])
    await user.click(within(panel).getByRole('button', { name: /^Replier/ }))
    const pill = within(dock).getByRole('button', { name: /déplier/ })
    expect(pill).toHaveTextContent('Travaille')
    expect(pill.querySelector('.sdot-w')).toHaveClass('sg-work')
  })

  it('the session ended, its wait kept: the sentence, the grey ring, the field kept (D29)', async () => {
    const { dock } = await openDock((d) => {
      d.peers = []
      d.waits = [arb({ session_ended: true })]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(panel).toHaveTextContent('Session terminée — ta réponse ira à sa prochaine session')
    expect(within(panel).getByRole('textbox', { name: 'Ta réponse' })).toBeInTheDocument()
    expect(panel.querySelector('.sd-h .sdot-w')).toHaveClass('sg-idle')
    expect(panel.querySelector('.sd-id span')).toHaveTextContent(/Terminée$/)
  })

  it('the session ended, an answer refused: the card stays mounted, text and choice back in the field, the failure shown (R19)', async () => {
    const c = await openDock((d) => {
      d.peers = []
      d.waits = [arb({ session_ended: true })]
    })
    // the refusal comes after the optimistic close has rendered
    c.setRoute('POST /waits/wait_open/answer', async () => {
      await new Promise((r) => setTimeout(r, 60))
      return { status: 409, body: { error: { code: 'CONFLICT', message: 'closed' } } }
    })
    const panel = await within(c.dock).findByRole('region', { name: /^Session / })
    await c.user.click(within(panel).getByRole('radio', { name: /^A · Plein écran/ }))
    await c.user.type(within(panel).getByRole('textbox', { name: 'Ta réponse' }), 'ma note')
    await c.user.click(within(panel).getByRole('button', { name: /Envoyer/ }))
    await waitFor(() => expect(c.writes().map((w) => `${w.method} ${w.url}`)).toEqual(['POST /waits/wait_open/answer']))
    expect(await within(panel).findByRole('alert')).toHaveTextContent("Ta réponse n'est pas partie")
    expect(within(panel).getByRole('textbox', { name: 'Ta réponse' })).toHaveValue('ma note')
    expect(within(panel).getByRole('radio', { name: /^A · Plein écran/ })).toHaveAttribute('aria-checked', 'true')
  })

  it('a native dialog (permission): what is asked, no field', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'waiting' })]
      d.waits = [arb({ type: 'permission', source: 'auto', reason: 'Bash', details: { cmd: 'rm -rf build' } })]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(panel).toHaveTextContent('rm -rf build')
    expect(within(panel).queryByRole('textbox')).toBeNull()
    expect(within(panel).queryByRole('radio')).toBeNull()
  })

  it('« Aller à la session » on a native dialog: POST /peers/{session}/reveal; a refusal says what to do', async () => {
    const c = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'waiting', cmux_workspace_id: 'AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE' })]
      d.waits = [arb({ type: 'permission', source: 'auto', reason: 'Bash', details: { cmd: 'rm -rf build' } })]
    })
    c.setRoute('POST /peers/sess-w/reveal', () => ({ status: 200, body: { data: { session_id: 'sess-w' } } }))
    const panel = await within(c.dock).findByRole('region', { name: /^Session / })
    await c.user.click(within(panel).getByRole('button', { name: 'Aller à la session' }))
    await waitFor(() => expect(c.writes().map((w) => `${w.method} ${w.url}`)).toEqual(['POST /peers/sess-w/reveal']))
    c.setRoute('POST /peers/sess-w/reveal', () => ({ status: 409, body: { error: { code: 'CONFLICT', message: 'its cmux tab is not open' } } }))
    await c.user.click(within(panel).getByRole('button', { name: 'Aller à la session' }))
    expect(await screen.findByText('Son onglet cmux est fermé : la session n\'a pas pu être affichée. Retrouve-la dans cmux.')).toBeInTheDocument()
  })

  it('« Aller à la session » refused with a 404 (unknown session): a neutral message, never « onglet cmux fermé »', async () => {
    const c = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'waiting', cmux_workspace_id: 'AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE' })]
      d.waits = [arb({ type: 'permission', source: 'auto', reason: 'Bash', details: { cmd: 'rm -rf build' } })]
    })
    c.setRoute('POST /peers/sess-w/reveal', () => ({ status: 404, body: { error: { code: 'NOT_FOUND', message: 'no such session' } } }))
    const panel = await within(c.dock).findByRole('region', { name: /^Session / })
    await c.user.click(within(panel).getByRole('button', { name: 'Aller à la session' }))
    expect(await screen.findByText(/session est introuvable/)).toBeInTheDocument()
    expect(screen.queryByText(/onglet cmux est fermé/)).toBeNull()
  })

  it('« Aller à la session » is on the native card only: not on an arbitrage, not without a cmux tab', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle', cmux_workspace_id: 'AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE' })]
      d.waits = [arb()]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(within(panel).getByRole('radio', { name: /^A · Plein écran/ })).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Aller à la session' })).toBeNull()
  })

  it('a native dialog of a session with no cmux tab: no button, the terminal sentence', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'waiting' })]
      d.waits = [arb({ type: 'permission', source: 'auto', reason: 'Bash', details: { cmd: 'ls' } })]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(panel).toHaveTextContent('Cette question se répond dans le terminal.')
    expect(within(panel).queryByRole('button', { name: 'Aller à la session' })).toBeNull()
  })

  it('`r` outside a field folds and unfolds, the focus into the field; Esc in the field folds, a second Esc closes the reader', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle' })]
      d.waits = [arb()]
    })
    await within(dock).findByRole('textbox', { name: 'Ta réponse' })
    ;(document.activeElement as HTMLElement | null)?.blur()
    fireEvent.keyDown(document.body, { key: 'r' })
    expect(within(dock).queryByRole('region', { name: /^Session / })).toBeNull()
    const pill = within(dock).getByRole('button', { name: /déplier \(r\)/ })
    expect(pill).toHaveTextContent('Arbitrage')
    fireEvent.keyDown(document.body, { key: 'r' })
    const field = within(dock).getByRole('textbox', { name: 'Ta réponse' })
    await waitFor(() => expect(field).toHaveFocus())
    // `r` typed in the field is a letter, not the key
    await user.keyboard('r')
    expect(field).toHaveValue('r')
    await user.keyboard('{Escape}')
    expect(getUi().reader).not.toBeNull()
    expect(within(dock).getByRole('button', { name: /déplier/ })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(getUi().reader).toBeNull()
  })

  it('the card moves by its header, bounded inside the reader under its bar', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle' })]
      d.waits = [arb()]
    })
    const card = await within(dock).findByRole('region', { name: /^Session / })
    expect(card).toHaveClass('sd-card')
    const box = card.parentElement?.closest('.rd-body') as HTMLElement
    expect(box).not.toBeNull()
    const rect = (x: number, y: number, w: number, h: number) => ({ x, y, left: x, top: y, width: w, height: h, right: x + w, bottom: y + h, toJSON: () => ({}) }) as DOMRect
    const spy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this === box) return rect(0, 44, 1440, 856)
      if (this === card) return rect(1064, 484, 360, 400)
      return rect(0, 0, 0, 0)
    })
    const head = card.querySelector('.sd-h') as HTMLElement
    fireEvent.pointerDown(head, { pointerId: 1, clientX: 1100, clientY: 500, button: 0 })
    fireEvent.pointerMove(head, { pointerId: 1, clientX: -900, clientY: -900 })
    expect(card.style.left).toBe('16px')
    expect(card.style.top).toBe('16px')
    fireEvent.pointerMove(head, { pointerId: 1, clientX: 9000, clientY: 9000 })
    expect(card.style.left).toBe('1064px')
    expect(card.style.top).toBe('440px')
    fireEvent.pointerUp(head, { pointerId: 1 })
    spy.mockRestore()
  })

  it('a dragged card is clamped again when the window shrinks (answer field and Send stay on screen)', async () => {
    const { dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle' })]
      d.waits = [arb()]
    })
    const card = await within(dock).findByRole('region', { name: /^Session / })
    const box = card.parentElement?.closest('.rd-body') as HTMLElement
    const rect = (x: number, y: number, w: number, h: number) => ({ x, y, left: x, top: y, width: w, height: h, right: x + w, bottom: y + h, toJSON: () => ({}) }) as DOMRect
    let bw = 1280
    const spy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this === box) return rect(0, 44, bw, 856)
      if (this === card) return rect(Number.parseFloat(card.style.left || '0'), 44 + Number.parseFloat(card.style.top || '0'), 360, 400)
      return rect(0, 0, 0, 0)
    })
    const head = card.querySelector('.sd-h') as HTMLElement
    card.style.left = '400px'
    card.style.top = '100px'
    fireEvent.pointerDown(head, { pointerId: 1, clientX: 100, clientY: 100, button: 0 })
    fireEvent.pointerMove(head, { pointerId: 1, clientX: 187, clientY: 100 })
    fireEvent.pointerUp(head, { pointerId: 1 })
    expect(card.style.left).toBe('487px')
    bw = 600
    act(() => setWidth(600))
    await waitFor(() => expect(card.style.left).toBe('224px'))
    spy.mockRestore()
  })

  it('the waits are re-read every 3 s (fake clock), never behind /peers (R33)', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { dock, fetchFn } = await openDock(
      (d) => {
        d.peers = [peer('sess-w', { status: 'idle' })]
        d.waits = [arb()]
      },
      1440,
    )
    await within(dock).findByRole('region', { name: /^Session / })
    const reads = () => fetchFn.mock.calls.filter((c) => String(c[0]) === '/waits?subject=pig-r1&closed=2').length
    const n = reads()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_000)
    })
    expect(reads()).toBeGreaterThanOrEqual(n + 1)
  })

  it('/peers hung: the waits still show (R33)', async () => {
    const data = makeBacklog()
    data.groups = { 'pig-r1': [group()] }
    data.outputs = { 'pig-r1': [] }
    data.waits = [arb()]
    const c = await renderApp(data, { hang: ['/peers?all=1'] })
    await c.user.click(groupButton('th:th_pig'))
    await c.user.click(document.querySelector('li[data-id="pig-r1"]') as HTMLElement)
    await c.user.click(await screen.findByRole('button', { name: 'outputs-consultables' }))
    const dlg = await screen.findByRole('dialog', { name: /Maquette outputs-consultables/ })
    expect(await within(dlg).findByRole('textbox', { name: 'Ta réponse' })).toBeInTheDocument()
  })

  it('no wait: the end of the session is read from /peers at its 10 s cadence (« Session terminée »)', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { dock, data } = await openDock((d) => {
      d.peers = [peer('sess-w')]
    })
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    await waitFor(() => expect(panel).toHaveTextContent('La session travaille'))
    data().peersAll = [peer('sess-w', { alive: false, ended_at: minutesAgo(0) })]
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000)
    })
    await waitFor(() => expect(within(dock).getByRole('region', { name: /^Session / })).toHaveTextContent('Session terminée'))
  })
})

describe('the dock in its three forms (D6): the dot and the pill whole (the ring that overflowed, sub_x000000000000009)', () => {
  it.each([
    [1800, 'sd-side'],
    [1440, 'sd-card'],
    [390, 'sd-sheet'],
  ])('%i px → %s: the head dot in its own box; folded, the pill carries its dot', async (width, cls) => {
    const { dock, user } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle' })]
      d.waits = [arb()]
    }, width)
    const panel = await within(dock).findByRole('region', { name: /^Session / })
    expect(panel).toHaveClass(cls)
    const dot = panel.querySelector('.sd-h .sdot-w')
    expect(dot).toHaveClass('sess-wait')
    expect(dot?.querySelector('.dot')).not.toBeNull()
    await user.click(within(panel).getByRole('button', { name: /^Replier/ }))
    const pill = within(dock).getByRole('button', { name: /déplier/ })
    expect(pill).toHaveClass('sd-pill')
    expect(pill.querySelector('.sdot-w.sess-wait .dot')).not.toBeNull()
  })

  const css = (f: string) => readFileSync(join(process.cwd(), 'src', f), 'utf8')
  const rule = (sel: string) =>
    blocks(css('reader/reader.css'))
      .filter((b) => b.selector.replace(/^@layer \w+ /, '').trim() === sel)
      .map((b) => b.body)
      .join(';')
  const tokens = css('theme/tokens.css')
  const px = (v: string): number => {
    const m = /var\((--[\w-]+)\)/.exec(v)
    if (!m) return Number.parseFloat(v)
    const d = new RegExp(`${m[1]}:\\s*([^;]+);`).exec(tokens)
    return d ? px(d[1].trim()) : NaN
  }
  const ring = px('var(--size-focus)') + 1 // globals.css: outline var(--size-focus), offset 1px

  it('the pill sits a ring clear of the reader edges, and nothing clips it (no overflow on the dock root)', () => {
    const pill = rule('.sd-pill')
    expect(px(/right:\s*([^;]+);/.exec(pill)![1])).toBeGreaterThanOrEqual(ring)
    expect(px(/bottom:\s*([^;]+);/.exec(pill)![1])).toBeGreaterThanOrEqual(ring)
    expect(rule('.sd')).not.toMatch(/overflow/)
    expect(rule('.rd-body')).not.toMatch(/overflow:\s*hidden/)
  })

  it('the dot box holds the dot and its halo; the head keeps a ring of padding inside the clipped card', () => {
    expect(px('var(--size-icon)')).toBeGreaterThanOrEqual(px('var(--size-sess-dot)') + 2 * px('var(--size-sess-halo)'))
    const pad = /padding:\s*([^;]+);/.exec(rule('.sd-h'))![1].split(/\s+(?![^(]*\))/)
    for (const p of pad) expect(px(p), p).toBeGreaterThanOrEqual(ring)
    const body = /padding:\s*([^;]+);/.exec(rule('.sd-b'))![1]
    expect(px(body)).toBeGreaterThanOrEqual(ring)
  })

  it('the tours menu passes over the dock: the bar (its host) stacks above every form, the body opens no stacking context', () => {
    const z = (sel: string) => Number(/z-index:\s*(\d+)/.exec(rule(sel))?.[1] ?? NaN)
    const bar = z('.rd-h')
    for (const sel of ['.sd-card', '.sd-sheet', '.sd-pill']) expect(bar, sel).toBeGreaterThanOrEqual(z(sel))
    expect(rule('.rd-body')).not.toMatch(/z-index/)
    expect(rule('.sd-side')).not.toMatch(/z-index/)
  })

  it('the dock body scrolls instead of squashing its children (a short window keeps options, note and Send reachable)', () => {
    // .wait is overflow:hidden and a flex item: left shrinkable it collapses to its padding at 390x300 and nothing scrolls
    expect(rule('.sd-b')).toMatch(/overflow-y:\s*auto/)
    expect(rule('.sd-b > *')).toMatch(/flex:\s*none/)
  })

  it('tab order: every control of the reader bar comes before the dock in the DOM', async () => {
    const { dlg, dock } = await openDock((d) => {
      d.peers = [peer('sess-w', { status: 'idle' })]
      d.waits = [arb()]
    })
    const bar = dlg.querySelector('.rd-h') as HTMLElement
    expect(bar.compareDocumentPosition(dock) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const order = [...dlg.querySelectorAll<HTMLElement>('button, a[href], textarea')].filter((x) => x.tabIndex >= 0)
    const lastBar = order.filter((x) => bar.contains(x)).at(-1)!
    const firstDock = order.find((x) => dock.contains(x))!
    expect(order.indexOf(lastBar)).toBeLessThan(order.indexOf(firstDock))
  })

  describe('round 1 captures (the rules; a browser proves the pixels)', () => {
    const sessionsCss = blocks(css('sessions/sessions.css'))
    const sRule = (sel: string) =>
      sessionsCss
        .filter((b) => b.selector.replace(/^@layer \w+ /, '').trim() === sel)
        .map((b) => b.body)
        .join(';')

    it('D: a selected option differs from the focus ring (no ink border, a filled dot instead)', () => {
      const sel = rule('.wa-o[aria-checked="true"]')
      expect(sel).not.toMatch(/border-color:\s*var\(--text\)/)
      expect(sel).toMatch(/border-color:\s*var\(--border-strong\)/)
      expect(rule('.wa-o[aria-checked="true"] .rad')).toMatch(/background:\s*var\(--text\)/)
    })

    it('E: the answered card carries no side stripe', () => {
      expect(rule('.wait.over')).not.toMatch(/inset/)
    })

    it('F: the field row and the Esc hint share the question’s left edge', () => {
      const left = (r: string) => /padding:\s*([^;]*)/.exec(r)?.[1].match(/calc\(.*\)/)?.[0]
      expect(left(rule('.wait-f'))).toBe(left(sRule('.wait-b')))
      expect(left(rule('.wait-f'))).toBeTruthy()
    })

    it('G: the drag grip is visible at rest and stronger on hover and focus', () => {
      expect(rule('.sd-grip')).toMatch(/color:\s*var\(--text-2\)/)
      expect(rule('.sd-card .sd-h:hover .sd-grip')).toMatch(/color:\s*var\(--text\)/)
      expect(rule('.sd-grip svg')).toMatch(/width:\s*var\(--size-icon\)/)
    })

    it('H: the reco chip and the kind pill have an edge of their own on the surface', () => {
      expect(rule('.wa-o .gm-tag')).toMatch(/box-shadow:\s*inset[^;]*var\(--border-strong\)/)
      expect(rule('.wait-h .kind')).toMatch(/box-shadow:\s*inset[^;]*var\(--accent-attn-line\)/)
    })

    it('I: the card keeps its shortcut hints below the phone breakpoint (the card form begins at 600 px)', () => {
      expect(rule('.phone .sd-card .ql-hint')).toMatch(/display:\s*inline-flex/)
    })

    it('J: the card shows its kbd below the phone breakpoint too (.phone kbd would hide them)', () => {
      expect(rule('.phone .sd-card kbd')).toMatch(/display:\s*inline-flex/)
    })
  })
})
