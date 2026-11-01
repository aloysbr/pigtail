import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import type { SdkRequest } from '@/api/types'
import { getToasts, resetToasts } from '@/ui/toasts'
import { FORBIDDEN, renderSdk } from './harness'
import { SdkRequestCard } from './SdkRequestCard'

beforeEach(resetToasts)

const NOW = Date.parse('2026-10-29T10:10:00Z')
const ANSWER = '/sdk-sessions/sdk_p1/requests/r1/answer'

function question(questions: unknown[]): SdkRequest {
  return { rid: 'r1', kind: 'question', tool: 'AskUserQuestion', input: { questions } as SdkRequest['input'], opened_at: '2026-10-29T10:00:05Z' }
}
const permission: SdkRequest = { rid: 'r1', kind: 'permission', tool: 'Bash', input: { command: 'rm -rf build' }, opened_at: '2026-10-29T10:00:05Z' }

const card = (r: SdkRequest) => renderSdk(<SdkRequestCard sessionId="sdk_p1" request={r} now={NOW} title="Sujet P" />)

describe('SdkRequestCard — question', () => {
  const three = question([{ question: 'Which colour?', header: 'Colour', multiSelect: false, options: [{ label: 'Red' }, { label: 'Green', description: 'g' }, { label: 'Blue' }] }])

  it('is a WaitCard: one button per option with the exact option text, then « Autre réponse… »', () => {
    card(three)
    const region = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(region).toHaveClass('wait')
    expect(region.querySelector('.wait-q')).toHaveTextContent('Which colour?')
    const names = [...region.querySelectorAll('.wait-f button')].map((b) => b.textContent)
    expect(names).toEqual(['Red', 'Green', 'Blue', 'Autre réponse…'])
  })

  it('a click on an option posts allow with answers keyed by the question text', async () => {
    const { posted } = card(three)
    await userEvent.click(screen.getByRole('button', { name: 'Green' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0]).toEqual({ url: ANSWER, body: { behavior: 'allow', answers: { 'Which colour?': 'Green' } } })
  })

  it('« Autre réponse… » opens a text field; « Envoyer » posts the free text as the answer', async () => {
    const { posted } = card(three)
    expect(screen.queryByRole('textbox')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Autre réponse…' }))
    await userEvent.type(screen.getByRole('textbox', { name: 'Autre réponse' }), 'Mauve')
    await userEvent.click(screen.getByRole('button', { name: 'Envoyer' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body).toEqual({ behavior: 'allow', answers: { 'Which colour?': 'Mauve' } })
  })

  it('multiSelect: a CheckboxGroup and « Envoyer » (off until one is ticked); the labels go as a list', async () => {
    const multi = question([{ question: 'Which fruits?', multiSelect: true, options: [{ label: 'Apple' }, { label: 'Banana' }] }])
    const { posted } = card(multi)
    expect(screen.getByRole('group', { name: 'Which fruits?' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Envoyer' })).toBeDisabled()
    await userEvent.click(screen.getByRole('checkbox', { name: 'Banana' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Apple' }))
    await userEvent.click(screen.getByRole('button', { name: 'Envoyer' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body).toEqual({ behavior: 'allow', answers: { 'Which fruits?': ['Apple', 'Banana'] } })
  })

  it('multiSelect: the question shows once (the group keeps its accessible name, visually hidden)', () => {
    const multi = question([{ question: 'Which fruits?', multiSelect: true, options: [{ label: 'Apple' }] }])
    const { container } = card(multi)
    const group = screen.getByRole('group', { name: 'Which fruits?' })
    const legend = group.querySelector('legend')
    expect(legend).toHaveClass('sr')
    const visible = [...container.querySelectorAll('*')].filter((el) => el.children.length === 0 && el.textContent === 'Which fruits?' && !el.closest('.sr'))
    expect(visible).toHaveLength(1)
  })

  it('multiSelect: options, then « Autre réponse… », then « Envoyer » (D19)', () => {
    const multi = question([{ question: 'Which fruits?', multiSelect: true, options: [{ label: 'Apple' }] }])
    card(multi)
    expect(screen.getAllByRole('button').map((b) => b.textContent)).toEqual(['Autre réponse…', 'Envoyer'])
  })

  it('aria-expanded buttons point at the region they unfold', async () => {
    const multi = question([{ question: 'Which fruits?', multiSelect: false, options: [{ label: 'Apple' }] }])
    card(multi)
    const other = screen.getByRole('button', { name: 'Autre réponse…' })
    await userEvent.click(other)
    const id = other.getAttribute('aria-controls')
    expect(id).toBeTruthy()
    expect(document.getElementById(id as string)).toContainElement(screen.getByRole('textbox', { name: 'Autre réponse' }))
  })

  it('several questions are asked one at a time, in order; one post at the end carries every answer', async () => {
    const two = question([
      { question: 'First?', multiSelect: false, options: [{ label: 'A' }, { label: 'B' }] },
      { question: 'Second?', multiSelect: false, options: [{ label: 'C' }, { label: 'D' }] },
    ])
    const { posted } = card(two)
    expect(screen.getByText('First?')).toBeInTheDocument()
    expect(screen.queryByText('Second?')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'B' }))
    expect(posted).toHaveLength(0)
    expect(screen.getByText('Second?')).toBeInTheDocument()
    expect(screen.queryByText('First?')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'C' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body).toEqual({ behavior: 'allow', answers: { 'First?': 'B', 'Second?': 'C' } })
  })

  it('a refused answer (409) shows the refusal toast and gives the buttons back', async () => {
    const { posted } = renderSdk(<SdkRequestCard sessionId="sdk_p1" request={three} now={NOW} title="Sujet P" />, { fail: { [ANSWER]: { status: 409 } } })
    await userEvent.click(screen.getByRole('button', { name: 'Red' }))
    await waitFor(() => expect(getToasts().length).toBeGreaterThan(0))
    expect(getToasts().at(-1)!.text).toMatch(/Sujet P/)
    expect(posted).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Red' })).not.toBeDisabled()
  })
})

describe('SdkRequestCard — permission', () => {
  it('shows the tool and its input, « Autoriser » and « Refuser »', () => {
    card(permission)
    const region = screen.getByRole('region', { name: 'Attend ta réponse' })
    expect(region.querySelector('.kind')).toHaveTextContent('Permission')
    expect(region).toHaveTextContent('Bash')
    expect(region).toHaveTextContent('rm -rf build')
    expect([...region.querySelectorAll('.wait-f button')].map((b) => b.textContent)).toEqual(['Autoriser', 'Refuser'])
  })

  it('« Autoriser » posts allow at once', async () => {
    const { posted } = card(permission)
    await userEvent.click(screen.getByRole('button', { name: 'Autoriser' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0]).toEqual({ url: ANSWER, body: { behavior: 'allow' } })
  })

  it('TestDenyNeedsConfirmation: « Refuser » unfolds « Motif » and « Confirmer le refus » and posts nothing; only the confirmation posts deny', async () => {
    const { posted } = card(permission)
    expect(screen.queryByRole('button', { name: 'Confirmer le refus' })).toBeNull()
    expect(screen.queryByRole('textbox', { name: 'Motif' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Refuser' }))
    expect(screen.getByRole('button', { name: 'Refuser' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('textbox', { name: 'Motif' })).toBeInTheDocument()
    expect(posted).toHaveLength(0)
    await userEvent.type(screen.getByRole('textbox', { name: 'Motif' }), 'trop risqué')
    expect(posted).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Confirmer le refus' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0]).toEqual({ url: ANSWER, body: { behavior: 'deny', message: 'trop risqué' } })
  })

  it('the reason is optional: confirming with an empty « Motif » posts deny with no message', async () => {
    const { posted } = card(permission)
    await userEvent.click(screen.getByRole('button', { name: 'Refuser' }))
    await userEvent.click(screen.getByRole('button', { name: 'Confirmer le refus' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body).toEqual({ behavior: 'deny' })
  })

  it('names no product', () => {
    const { container } = card(permission)
    expect(container.textContent).not.toMatch(FORBIDDEN)
  })
})
