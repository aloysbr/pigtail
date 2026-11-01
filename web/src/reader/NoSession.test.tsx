import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Subject } from '@/api/types'
import { resetLaunches } from '@/list/launch'
import { minutesAgo, type BacklogData } from '@/test/backlog'
import { openDock, past, peer } from '@/test/dock'
import type { Reply } from '@/test/render'
import { blocks } from '@/theme/css-parse'
import { resetToasts } from '@/ui/toasts'

afterEach(() => {
  vi.useRealTimers()
  resetToasts()
  resetLaunches()
})

const INVITE = 'Aucune session en cours. Lance-en une sur ce sujet avec le bouton ci-dessus.'

function setStatus(d: BacklogData, id: string, status: string) {
  d.subjects = d.subjects.map((x): Subject => (x.id === id ? { ...x, status } : x))
}

/** pig-r1 turned Done while its deliverable is open (a Done subject is not launchable: canLaunch false). */
async function done(c: { data(): BacklogData; refetch(): Promise<void> }) {
  setStatus(c.data(), 'pig-r1', 'done')
  await act(async () => {
    await c.refetch()
  })
}

async function unfold(dock: HTMLElement, user: { click(e: Element): Promise<void> }) {
  await user.click(await within(dock).findByRole('button', { name: /^Aucune session, déplier/ }))
  return within(dock).getByRole('region', { name: 'Aucune session' })
}

describe('NoSession (D8): no session on the subject', () => {
  it('opens folded: the pill « Aucune session », the grey ring, no panel', async () => {
    const { dock } = await openDock((d) => {
      d.peers = []
    })
    const pill = await within(dock).findByRole('button', { name: /^Aucune session, déplier \(r\)$/ })
    expect(pill).toHaveTextContent('Aucune session')
    expect(pill.querySelector('.sdot-w.sg-idle .dot')).not.toBeNull()
    expect(pill.classList.contains('sess-wait')).toBe(false)
    expect(within(dock).queryByRole('region')).toBeNull()
  })

  it('unfolded: « Sessions en cours » of the scope that are free, waiting first, then working, then resting; another scope and the ended left out, the ones bound to a subject kept (D12)', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [
        peer('s-work', { subject_id: undefined, cell_id: 'w1', status: 'busy' }),
        peer('s-idle', { subject_id: undefined, cell_id: 'i1', status: 'idle' }),
        peer('s-wait', { subject_id: undefined, cell_id: 'a1', status: 'waiting' }),
        peer('s-work', { subject_id: undefined, scope: 'globex', cell_id: 't1' }),
        peer('s-end', { subject_id: undefined, cell_id: 'e1', alive: false, ended_at: new Date().toISOString() }),
        peer('s-bound', { subject_id: 'pig-r2', cell_id: 'b1', status: 'busy' }),
      ]
    })
    const panel = await unfold(dock, user)
    const list = within(panel).getByRole('list', { name: 'Sessions en cours' })
    const ids = [...list.querySelectorAll<HTMLElement>('li.pk')].map((li) => li.dataset.sid)
    expect(ids).toEqual(['s-wait', 's-work', 's-bound', 's-idle'])
  })

  it('one « Lancer » button only, at the head', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = []
    })
    const panel = await unfold(dock, user)
    expect(within(panel).getAllByRole('button', { name: /Lancer/ })).toHaveLength(1)
    expect(within(panel).getByRole('button', { name: 'Lancer une session sur ce sujet' })).toBeInTheDocument()
  })

  it('choosing a session in the list shows it in the panel', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [peer('s-o', { subject_id: undefined, cell_id: 'o1', cmux_title: 'Relecture écrans' })]
    })
    const panel = await unfold(dock, user)
    await user.click(within(panel).getByRole('button', { name: /Relecture écrans/ }))
    const shown = await within(dock).findByRole('region', { name: 'Session Relecture écrans' })
    expect(shown).toHaveTextContent('La session travaille')
  })

  it('a launched session shows in the panel as soon as /peers carries it for the subject', async () => {
    const c = await openDock((d) => {
      d.peers = []
    })
    // no launch id to follow: useLaunch re-reads /peers at once; the stub's next read carries the new session
    c.setRoute('POST /subjects/pig-r1/launch', (): Reply => {
      c.data().peers = [peer('s-new', { cell_id: 'n9', cmux_title: 'Nouvelle n9' })]
      return { status: 202, body: { data: { launch_id: '', subject_id: 'pig-r1', state: 'pending', cell: 'mp' } } }
    })
    const panel = await unfold(c.dock, c.user)
    await c.user.click(within(panel).getByRole('button', { name: 'Lancer une session sur ce sujet' }))
    expect(c.writes().filter((w) => w.method === 'POST' && w.url === '/subjects/pig-r1/launch')).toHaveLength(1)
    expect(await within(c.dock).findByRole('region', { name: 'Session Nouvelle n9' })).toHaveTextContent('La session travaille')
  })

  it('a subject that cannot be launched (canLaunch false): no button', async () => {
    const c = await openDock((d) => {
      d.peers = [peer('s-o', { subject_id: undefined, cell_id: 'o1', cmux_title: 'Relecture écrans' })]
    })
    await done(c)
    const panel = await unfold(c.dock, c.user)
    expect(within(panel).queryByRole('button', { name: /Lancer/ })).toBeNull()
    expect(within(panel).getByRole('button', { name: /Relecture écrans/ })).toBeInTheDocument()
  })

  it('empty list: the sentence that says what happens and what to do', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = []
    })
    const panel = await unfold(dock, user)
    expect(within(panel).getByText(INVITE)).toBeInTheDocument()
  })

  it('empty list and a subject that cannot be launched: « Aucune session en cours. » alone, no invitation (G8)', async () => {
    const c = await openDock((d) => {
      d.peers = []
    })
    await done(c)
    const panel = await unfold(c.dock, c.user)
    expect(within(panel).getByText('Aucune session en cours.')).toBeInTheDocument()
    expect(panel).not.toHaveTextContent('Lance-en une')
    expect(within(panel).queryByRole('button', { name: /Lancer/ })).toBeNull()
  })

  it('a session of another subject chosen: its own waits and last exchange (?session=), never the subject’s', async () => {
    const c = await openDock((d) => {
      d.peers = [peer('s-o', { subject_id: 'pig-r2', cell_id: 'o1', cmux_title: 'Relecture écrans' })]
      d.waits = [past('w-mine', 's-gone', 30, 'Question du sujet ?', 'Réponse du sujet'), past('w-other', 's-o', 3, 'Question de l’autre ?', 'Réponse de l’autre', 'pig-r2')]
    })
    const panel = await unfold(c.dock, c.user)
    await c.user.click(within(panel).getByRole('button', { name: /Relecture écrans/ }))
    const shown = await within(c.dock).findByRole('region', { name: 'Session Relecture écrans' })
    await waitFor(() => expect(shown).toHaveTextContent('Question de l’autre ?'))
    expect(shown).toHaveTextContent('Réponse de l’autre')
    expect(shown).not.toHaveTextContent('Question du sujet ?')
    expect(c.fetchFn.mock.calls.some((x) => String(x[0]) === '/waits?session=s-o&closed=2')).toBe(true)
  })

  it('a launch in flight (useLaunching): the button busy « Lancement… », a second press sends nothing', async () => {
    const c = await openDock((d) => {
      d.peers = []
    })
    c.setRoute('POST /subjects/pig-r1/launch', () => new Promise<Reply>(() => {}))
    const panel = await unfold(c.dock, c.user)
    await c.user.click(within(panel).getByRole('button', { name: 'Lancer une session sur ce sujet' }))
    const busy = await within(panel).findByRole('button', { name: 'Lancement…' })
    expect(busy).toHaveAttribute('aria-busy', 'true')
    expect(busy).toBeDisabled()
    await act(async () => {
      busy.click()
    })
    expect(within(panel).getAllByRole('button', { name: /Lancer|Lancement/ })).toHaveLength(1)
    expect(c.writes().filter((w) => w.url === '/subjects/pig-r1/launch')).toHaveLength(1)
  })
})

describe('NoSession picker (round 1): real buttons, readable names, free sessions', () => {
  const free = (id: string, extra: Parameters<typeof peer>[1] = {}) => peer(id, { subject_id: undefined, ...extra })

  it('a session bound to another subject is offered too (D12), with its subject’s short title instead of « Libre »', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [free('s-free', { cmux_title: 'Libre un' }), peer('s-bound', { subject_id: 'pig-r2', subject_short_title: 'Palette Atlas', cmux_title: 'Prise ailleurs' })]
    })
    const panel = await unfold(dock, user)
    expect(within(panel).getByRole('button', { name: /Libre un/ })).toBeInTheDocument()
    const bound = within(panel).getByRole('button', { name: /Palette Atlas/ })
    expect(bound).toHaveTextContent('Palette Atlas')
    expect(bound).not.toHaveTextContent('Libre')
  })

  it('a bound session already named by its subject does not repeat the subject in the meta line', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [peer('s-bound', { subject_id: 'pig-r2', subject_short_title: 'Palette Atlas', cmux_title: undefined, name: undefined })]
    })
    const panel = await unfold(dock, user)
    const bound = within(panel).getByRole('button', { name: /Palette Atlas/ })
    expect(bound.textContent?.match(/Palette Atlas/g)).toHaveLength(1)
  })

  it('each row is a real button: Tab reaches it, Enter and Space pick the session, the li is no tab stop', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [free('s-a', { cmux_title: 'Alpha' }), free('s-b', { cmux_title: 'Beta' })]
    })
    const panel = await unfold(dock, user)
    const rows = panel.querySelectorAll<HTMLElement>('li.pk')
    expect(rows).toHaveLength(2)
    for (const li of rows) {
      expect(li.hasAttribute('tabindex')).toBe(false)
      expect(li.querySelector('button')).not.toBeNull()
    }
    const alpha = within(panel).getByRole('button', { name: /Alpha/ })
    alpha.focus()
    await user.keyboard('{Enter}')
    expect(await within(dock).findByRole('region', { name: 'Session Alpha' })).toBeInTheDocument()
  })

  it('Space picks too', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [free('s-a', { cmux_title: 'Alpha' })]
    })
    const panel = await unfold(dock, user)
    within(panel).getByRole('button', { name: /Alpha/ }).focus()
    await user.keyboard(' ')
    expect(await within(dock).findByRole('region', { name: 'Session Alpha' })).toBeInTheDocument()
  })

  it('a row tells the sessions apart: name, « Libre », the state once, the whole question', async () => {
    const reason = 'Faut-il garder le calque plein écran ou une fenêtre séparée ?'
    const { dock, user } = await openDock((d) => {
      d.peers = [
        free('s-idle', { cmux_title: 'Relecture écrans', status: 'idle' }),
        free('s-wait', { cmux_title: 'Atlas web', status: 'waiting', waits: [{ type: 'question', reason, source: 'declared', since: minutesAgo(3) }] }),
      ]
    })
    const panel = await unfold(dock, user)
    const idle = panel.querySelector('li.pk[data-sid="s-idle"]') as HTMLElement
    expect(idle).toHaveTextContent('Relecture écrans')
    expect(idle).toHaveTextContent('Libre')
    expect(idle.textContent).not.toMatch(/Inactive · Vivante/)
    expect((idle.textContent?.match(/Inactive/g) ?? []).length).toBe(1)
    const wait = panel.querySelector('li.pk[data-sid="s-wait"]') as HTMLElement
    expect(wait).toHaveTextContent(reason)
  })

  it('the picked session is named like `pigtail session ls` (short title, tab title, short id), never the raw id', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [free('33333333-aaaa', { cmux_title: 'Relecture écrans' }), free('44444444-bbbb')]
    })
    const panel = await unfold(dock, user)
    await user.click(within(panel).getByRole('button', { name: /Relecture écrans/ }))
    const shown = await within(dock).findByRole('region', { name: 'Session Relecture écrans' })
    expect(shown).not.toHaveTextContent('33333333')
    await user.click(within(shown).getByRole('button', { name: 'Choisir une autre session' }))
    const again = await within(dock).findByRole('region', { name: 'Aucune session' })
    await user.click(within(again).getByRole('button', { name: /44444444/ }))
    expect(await within(dock).findByRole('region', { name: 'Session 44444444' })).toBeInTheDocument()
  })

  it('the picked header names the session once, then its state in words (no repeated subject)', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [free('s-a', { cmux_title: 'Relecture écrans' })]
    })
    const panel = await unfold(dock, user)
    await user.click(within(panel).getByRole('button', { name: /Relecture écrans/ }))
    const shown = await within(dock).findByRole('region', { name: 'Session Relecture écrans' })
    expect(shown.querySelector('.sd-id span')).toHaveTextContent(/^Travaille · Vivante · Relecture écrans$/)
  })

  it('no empty body band between the header and the message of a working session', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = [free('s-a', { cmux_title: 'Alpha' })]
    })
    const panel = await unfold(dock, user)
    await user.click(within(panel).getByRole('button', { name: /Alpha/ }))
    const shown = await within(dock).findByRole('region', { name: 'Session Alpha' })
    expect(shown.querySelector('.sd-b')).toBeNull()
    expect(shown).toHaveTextContent('La session travaille')
  })

  it('« Lancer une session » is a button with an icon, not a full-width field look', async () => {
    const { dock, user } = await openDock((d) => {
      d.peers = []
    })
    const panel = await unfold(dock, user)
    const b = within(panel).getByRole('button', { name: 'Lancer une session sur ce sujet' })
    expect(b).toHaveClass('sd-launch')
    expect(b.querySelector('svg')).not.toBeNull()
  })
})

describe('NoSession picker CSS (a browser proves the pixels)', () => {
  const css = readFileSync(join(process.cwd(), 'src/reader/reader.css'), 'utf8')
  const rule = (sel: string) =>
    blocks(css)
      .filter((b) => b.selector.replace(/^@layer \w+ /, '').trim() === sel)
      .map((b) => b.body)
      .join(';')

  it('the launch button keeps its own width; the question wraps (never one ellipsed line); the row is a pointer button', () => {
    expect(rule('.sd-launch')).toMatch(/align-self:\s*flex-start/)
    expect(rule('.pk-q')).toMatch(/overflow-wrap:\s*anywhere/)
    expect(rule('.pk-q')).not.toMatch(/nowrap|ellipsis/)
    expect(rule('.pk-b')).toMatch(/cursor:\s*pointer/)
  })
})

describe('NoSession in the three forms (D6): the dot and the pill « Aucune session » whole (the ring that overflowed)', () => {
  it.each([
    [1800, 'sd-side'],
    [1440, 'sd-card'],
    [390, 'sd-sheet'],
  ])('%i px → %s: the pill and the panel head carry the grey ring dot', async (width, cls) => {
    const { dock, user } = await openDock((d) => {
      d.peers = []
    }, width)
    const pill = await within(dock).findByRole('button', { name: /^Aucune session, déplier/ })
    expect(pill.classList.contains('sd-pill')).toBe(true)
    expect(pill.querySelector('.sdot-w.sg-idle .dot')).not.toBeNull()
    const panel = await unfold(dock, user)
    expect(panel.classList.contains(cls)).toBe(true)
    expect(panel.querySelector('.sd-h .sdot-w.sg-idle .dot')).not.toBeNull()
    expect(screen.queryAllByRole('region', { name: 'Aucune session' })).toHaveLength(1)
  })
})
