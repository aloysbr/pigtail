import { useId, useState } from 'react'
import type { SdkQuestion, SdkRequest } from '@/api/types'
import { CheckboxGroup } from '@/ui/CheckboxGroup'
import { TextField } from '@/ui/TextField'
import { WaitCard } from '@/ui/WaitCard'
import { useSdkActions } from './api'
import { useGuarded } from './guarded'

type Answers = Record<string, string | string[]>

/** One line of the tool's input a person can judge: the command or the path when the input has one, else the input as JSON (bounded). */
function inputSummary(input: SdkRequest['input']): string {
  for (const k of ['command', 'file_path', 'path', 'url', 'pattern']) {
    const v = input[k]
    if (typeof v === 'string' && v) return v
  }
  const j = JSON.stringify(input)
  return j.length > 400 ? `${j.slice(0, 400)}…` : j
}

/**
 * The card of a pending request of a SDK session (D19): a WaitCard whose actions are the answer. A question shows one button per
 * option (its exact text), « Autre réponse… » for a free text, a checkbox group and « Envoyer » for `multiSelect`; several
 * questions are asked one at a time and answered in one post. A permission shows « Autoriser » and « Refuser »; « Refuser » only
 * unfolds the optional « Motif » and « Confirmer le refus », which alone posts the deny.
 */
export function SdkRequestCard({ sessionId, request, now, title, scope }: { sessionId: string; request: SdkRequest; now: number; title: string; scope?: string }) {
  return request.kind === 'permission' ? (
    <PermissionCard sessionId={sessionId} request={request} now={now} title={title} scope={scope} />
  ) : (
    <QuestionCard sessionId={sessionId} request={request} now={now} title={title} scope={scope} />
  )
}

interface Props {
  sessionId: string
  request: SdkRequest
  now: number
  title: string
  scope?: string
}

function PermissionCard({ sessionId, request, now, title, scope }: Props) {
  const { answer } = useSdkActions()
  const { busy, run } = useGuarded(title, scope)
  const [denying, setDenying] = useState(false)
  const denyId = useId()
  const [reason, setReason] = useState('')
  const post = (body: Parameters<typeof answer>[2]) => run(() => answer(sessionId, request.rid, body))
  return (
    <WaitCard type="permission" reason={`Permission demandée : ${request.tool}`} since={request.opened_at} now={now}>
      <code className="sdk-in">{inputSummary(request.input)}</code>
      <div className="sdk-acts">
        <button type="button" className="btnl" disabled={busy} onClick={() => void post({ behavior: 'allow' })}>
          Autoriser
        </button>
        <button type="button" className="btnl" disabled={busy} aria-expanded={denying} aria-controls={denyId} onClick={() => setDenying((d) => !d)}>
          Refuser
        </button>
      </div>
      {denying ? (
        <div id={denyId} className="sdk-reply">
          <TextField label="Motif" value={reason} onChange={setReason} disabled={busy} autoFocus />
          <div className="sdk-acts">
            <button
              type="button"
              className="btnl"
              disabled={busy}
              onClick={() => void post(reason.trim() ? { behavior: 'deny', message: reason.trim() } : { behavior: 'deny' })}
            >
              Confirmer le refus
            </button>
          </div>
        </div>
      ) : null}
    </WaitCard>
  )
}

function QuestionCard({ sessionId, request, now, title, scope }: Props) {
  const { answer } = useSdkActions()
  const { busy, run } = useGuarded(title, scope)
  const questions: SdkQuestion[] = request.input.questions ?? []
  const [step, setStep] = useState(0)
  const [done, setDone] = useState<Answers>({})
  const [other, setOther] = useState(false)
  const [text, setText] = useState('')
  const [picked, setPicked] = useState<string[]>([])
  const otherId = useId()
  const q = questions[step]
  if (!q) return null

  const give = async (value: string | string[]) => {
    const next = { ...done, [q.question]: value }
    if (step + 1 < questions.length) {
      setDone(next)
      setStep(step + 1)
      setOther(false)
      setText('')
      setPicked([])
      return
    }
    await run(() => answer(sessionId, request.rid, { behavior: 'allow', answers: next }))
  }

  return (
    <WaitCard type="question" reason={q.question} since={request.opened_at} now={now}>
      {questions.length > 1 ? (
        <p className="muted sdk-step">
          Question {step + 1} sur {questions.length}
        </p>
      ) : null}
      {q.multiSelect && !other ? (
        <CheckboxGroup legend={q.question} options={q.options.map((o) => ({ value: o.label, label: o.label, description: o.description }))} value={picked} onChange={setPicked} disabled={busy} hideLegend />
      ) : null}
      <div className="sdk-acts">
        {q.multiSelect
          ? null
          : q.options.map((o) => (
              <button key={o.label} type="button" className="btnl" disabled={busy} title={o.description} onClick={() => void give(o.label)}>
                {o.label}
              </button>
            ))}
        <button type="button" className="btnl" disabled={busy} aria-expanded={other} aria-controls={otherId} onClick={() => setOther((o) => !o)}>
          Autre réponse…
        </button>
        {q.multiSelect && !other ? (
          <button type="button" className="btnl" disabled={busy || picked.length === 0} onClick={() => void give(picked)}>
            Envoyer
          </button>
        ) : null}
      </div>
      {other ? (
        <div id={otherId} className="sdk-reply">
          <TextField label="Autre réponse" value={text} onChange={setText} onSubmit={(v) => void give(v.trim())} disabled={busy} autoFocus />
          <div className="sdk-acts">
            <button type="button" className="btnl" disabled={busy || text.trim() === ''} onClick={() => void give(text.trim())}>
              Envoyer
            </button>
          </div>
        </div>
      ) : null}
    </WaitCard>
  )
}
