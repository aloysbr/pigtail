import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import type { SdkSession, SdkState } from '@/api/types'
import { getToasts, resetToasts } from '@/ui/toasts'
import events from './fixtures/events.json'
import list from './fixtures/list.json'
import off from './fixtures/off.json'
import { FORBIDDEN, json, renderSdk } from './harness'
import { LaunchHereButton, SdkSessionBlock } from './SdkSessionBlock'

beforeEach(resetToasts)

const NOW = Date.parse('2026-10-29T10:10:00Z')
const LIST = '/sdk-sessions?subject=sub_p'
const base = list.data.sessions[0] as unknown as SdkSession
const ended = list.data.sessions[1] as unknown as SdkSession

const session = (state: SdkState, extra: Partial<SdkSession> = {}): SdkSession => ({ ...base, id: 'sdk_x', state, pending: [], live: true, ...extra })

function reads(sessions: SdkSession[], launch_enabled = true) {
  const r: Record<string, () => Response | Promise<Response>> = {
    [LIST]: () => json({ data: { sessions, launch_enabled }, meta: {} }),
  }
  for (const s of sessions) r[`/sdk-sessions/${s.id}/events?after=0`] = () => json(events)
  return r
}

const block = (sessions: SdkSession[], opts: Parameters<typeof renderSdk>[1] = {}) =>
  renderSdk(<SdkSessionBlock subjectId="sub_p" title="Sujet P" now={NOW} />, { reads: reads(sessions), ...opts })

describe('SdkSessionBlock — state', () => {
  it.each<[SdkState, string]>([
    ['starting', 'Démarre'],
    ['running', 'Travaille'],
    ['waiting', 'Attend ta réponse'],
    ['idle', 'Tour terminé'],
    ['ended', 'Terminée'],
    ['failed', 'Échec'],
    ['lost', 'Perdue'],
  ])('%s reads « %s » in words, the colour only accompanies', async (state, label) => {
    block([session(state, { live: !['ended', 'failed', 'lost'].includes(state), pending: [] })])
    const chip = await screen.findByText(label, { selector: '.sdk-st' })
    expect(chip).toHaveAttribute('data-state', state)
    expect(chip.textContent).toBe(label)
  })

  it('shows nothing when the option is off (503) and nothing when there is no session', async () => {
    const o = renderSdk(<SdkSessionBlock subjectId="sub_p" title="Sujet P" now={NOW} />, { reads: { [LIST]: () => json(off, 503) } })
    await waitFor(() => expect(o.fetchFn).toHaveBeenCalled())
    expect(o.container.querySelector('.sdk-blk')).toBeNull()
    o.unmount()
    const e = block([])
    await waitFor(() => expect(e.fetchFn).toHaveBeenCalled())
    expect(e.container.querySelector('.sdk-blk')).toBeNull()
  })

  it('says it when the sessions cannot be read (503 other than SDK_OFF), instead of reading as off', async () => {
    const unreadable = { error: { code: 'REGISTRY_UNREADABLE', message: 'unreadable' }, meta: { request_id: 'r' } }
    renderSdk(<SdkSessionBlock subjectId="sub_p" title="Sujet P" now={NOW} />, { reads: { [LIST]: () => json(unreadable, 503) } })
    expect(await screen.findByRole('alert')).toHaveTextContent('Impossible de lire')
  })

  it('names no product in its texts', async () => {
    const { container } = block([session('idle')])
    await screen.findByText('Tour terminé')
    expect(container.textContent).not.toMatch(FORBIDDEN)
  })
})

describe('SdkSessionBlock — thread', () => {
  it('lists the events, the most recent at the bottom, after a skeleton on the first load', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const r = reads([session('running')])
    r['/sdk-sessions/sdk_x/events?after=0'] = async () => {
      await gate
      return json(events)
    }
    const { container } = block([session('running')], { reads: r })
    const log = await screen.findByRole('log', { name: 'Fil de la session' })
    expect(log).toHaveAttribute('aria-busy', 'true')
    expect(container.querySelectorAll('.sdk-sk')).toHaveLength(3)
    release()
    await waitFor(() => expect(log.querySelectorAll('li')).toHaveLength(4))
    expect([...log.querySelectorAll('li')].map((li) => li.getAttribute('data-seq'))).toEqual(['1', '2', '3', '4'])
  })
})

describe('SdkSessionBlock — waiting', () => {
  it('shows the request card of a pending request', async () => {
    block([session('waiting', { pending: base.pending })])
    const card = await screen.findByRole('region', { name: 'Attend ta réponse' })
    expect(card.querySelector('.wait-q')).toHaveTextContent('Which fruits?')
    expect(within(card).getByRole('group', { name: 'Which fruits?' })).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Ta réponse' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Terminer la session' })).toBeNull()
  })
})

describe('SdkSessionBlock — idle', () => {
  it('« Ta réponse » and « Envoyer » post messages; « Terminer la session » posts end', async () => {
    const { posted } = block([session('idle')])
    await userEvent.type(await screen.findByRole('textbox', { name: 'Ta réponse' }), 'continue{Enter}')
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0]).toEqual({ url: '/sdk-sessions/sdk_x/messages', body: { text: 'continue' } })
    await userEvent.type(screen.getByRole('textbox', { name: 'Ta réponse' }), 'encore')
    await userEvent.click(screen.getByRole('button', { name: 'Envoyer' }))
    await waitFor(() => expect(posted).toHaveLength(2))
    await userEvent.click(screen.getByRole('button', { name: 'Terminer la session' }))
    await waitFor(() => expect(posted).toHaveLength(3))
    expect(posted[2]).toEqual({ url: '/sdk-sessions/sdk_x/end', body: undefined })
  })

  it('« Envoyer » is off while the field is empty', async () => {
    block([session('idle')])
    await screen.findByRole('textbox', { name: 'Ta réponse' })
    expect(screen.getByRole('button', { name: 'Envoyer' })).toBeDisabled()
  })

  it('no resume command while idle (a writer is alive)', async () => {
    block([session('idle', { resume_command: undefined })])
    await screen.findByText('Tour terminé')
    expect(screen.queryByText('Reprendre dans un terminal')).toBeNull()
  })

  it.each<SdkState>(['starting', 'running', 'waiting', 'ended', 'failed', 'lost'])('the reply field and « Terminer la session » are absent in %s', async (state) => {
    block([session(state, { live: state === 'starting' || state === 'running' || state === 'waiting' })])
    await screen.findByRole('log', { name: 'Fil de la session' })
    expect(screen.queryByRole('textbox', { name: 'Ta réponse' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Terminer la session' })).toBeNull()
  })
})

describe('SdkSessionBlock — resume', () => {
  it.each<SdkState>(['ended', 'lost'])('%s with a resume command: « Reprendre dans un terminal » and the command in a selectable <code>', async (state) => {
    block([{ ...ended, state, live: false }])
    const sec = await screen.findByRole('group', { name: 'Reprendre dans un terminal' })
    const code = sec.querySelector('code')!
    expect(code).toHaveTextContent('cd /work/p && CLAUDE_CONFIG_DIR=/cell/mp claude --resume csid-p2')
    expect(sec.querySelector('button')).toBeNull() // no button that opens a terminal
  })

  it('a failed session says why', async () => {
    block([session('failed', { live: false, reason: 'host exited before ready' })])
    expect(await screen.findByText(/host exited before ready/)).toBeInTheDocument()
  })
})

describe('LaunchHereButton', () => {
  const props = { subjectId: 'sub_p', title: 'Sujet P', scope: 'perso' }

  it('is absent when the option is off', async () => {
    const o = renderSdk(<LaunchHereButton {...props} />, { reads: { [LIST]: () => json(off, 503) } })
    await waitFor(() => expect(o.fetchFn).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: 'Lancer ici' })).toBeNull()
  })

  it('is absent when launch_enabled is false (CA14)', async () => {
    const o = renderSdk(<LaunchHereButton {...props} />, { reads: reads([], false) })
    await waitFor(() => expect(o.fetchFn).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByRole('button', { name: 'Lancer ici' })).toBeNull()
  })

  it('is absent while a session of the subject is live', async () => {
    const o = renderSdk(<LaunchHereButton {...props} />, { reads: reads([session('idle')]) })
    await waitFor(() => expect(o.fetchFn).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByRole('button', { name: 'Lancer ici' })).toBeNull()
  })

  it('posts the launch, then re-reads the sessions', async () => {
    const o = renderSdk(<LaunchHereButton {...props} />, { reads: reads([]) })
    await userEvent.click(await screen.findByRole('button', { name: 'Lancer ici' }))
    await waitFor(() => expect(o.posted).toEqual([{ url: '/subjects/sub_p/sdk-sessions', body: undefined }]))
    await waitFor(() => expect(o.fetchFn.mock.calls.filter(([u]) => String(u) === LIST).length).toBeGreaterThan(1))
  })

  it('a 409 goes through the refusal toast', async () => {
    renderSdk(<LaunchHereButton {...props} />, { reads: reads([]), fail: { '/subjects/sub_p/sdk-sessions': { status: 409, code: 'SESSION_LIVE' } } })
    await userEvent.click(await screen.findByRole('button', { name: 'Lancer ici' }))
    await waitFor(() => expect(getToasts().length).toBeGreaterThan(0))
    expect(getToasts().at(-1)!.text).toMatch(/Une session est déjà ouverte pour « Sujet P »/)
    expect(screen.getByRole('button', { name: 'Lancer ici' })).not.toBeDisabled()
  })
})
