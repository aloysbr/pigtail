import { useState, type KeyboardEvent, type ReactNode, type Ref } from 'react'
import { ApiError } from '@/api/client'
import { answerFailed } from '@/api/refusals'
import type { Wait, WaitAnswerRequest } from '@/api/types'
import { answerText, useAnswerWait } from '@/api/waits'
import { ageOf } from '@/domain/age'
import { hhmm, waitLabel } from '@/domain/sessions'

/** The read-only card: the type, the reason and since when (the Sessions panel). */
interface ReadProps {
  variant?: 'read'
  type: string
  reason?: string
  since: string
  now: number
  /** An action slot under the reason (.wait-f), absent when there are none: a SDK session's request puts its buttons there. */
  children?: ReactNode
}

/** The card that answers (T12): one wait record as GET /waits gives it. */
interface AnswerProps {
  variant: 'answer'
  wait: Wait
  now: number
  /** The field, for the dock's `r` (focus into it). */
  fieldRef?: Ref<HTMLTextAreaElement>
  /** Esc in the field: the dock folds (« Esc replier »); absent, Esc is left to the page. */
  onFieldEscape?: () => void
  /** The session ended (the dock's `ended`): an answered card then says the answer waits for its next session. */
  ended?: boolean
}

/**
 * The wait card (t3 .wait, app.css:295): « Attend ta réponse · depuis … », the type chip (.wait-h .kind), then the reason.
 * Read only by default (no option, no « Répondre »: absent, never greyed); `variant="answer"` answers it (T12).
 */
export function WaitCard(props: ReadProps | AnswerProps) {
  if (props.variant === 'answer') return <AnswerCard {...props} />
  const { type, reason, since, now, children } = props
  const age = ageOf(since, now)
  return (
    <section className="wait" aria-label="Attend ta réponse">
      <div className="wait-h">
        Attend ta réponse
        {age ? <span className="muted">· depuis {age}</span> : null}
        <span className="kind">{waitLabel(type)}</span>
      </div>
      {reason ? (
        <div className="wait-b">
          <p className="wait-q">{reason}</p>
        </div>
      ) : null}
      {children ? <div className="wait-f">{children}</div> : null}
    </section>
  )
}

/** How a wait is answered from the page. native: a Claude Code dialog, shown, answered in its terminal only. */
export type AnswerForm = 'native' | 'choice' | 'text' | 'items' | 'seen' | 'none'

/**
 * D9, the one switch point of a review's answer: a review with items shows them with no field (like a native dialog),
 * a review without items is answered « Vu » or with a text.
 */
export function reviewForm(w: Wait): 'items' | 'seen' {
  return (w.details?.items?.length ?? 0) > 0 ? 'items' : 'seen'
}

/**
 * The answer form of a wait. An auto wait (any type) or a permission is native: POST /waits/{id}/answer refuses them
 * (source=auto 403, waits_answer.go), so neither option nor field shows. The forms follow internal/waitdetail acceptedForms.
 */
export function answerForm(w: Wait): AnswerForm {
  if (w.source === 'auto' || w.type === 'permission') return 'native'
  switch (w.type) {
    case 'arbitrage':
      return 'choice'
    case 'question':
      return (w.details?.options?.length ?? 0) > 0 ? 'choice' : 'text'
    case 'review':
      return reviewForm(w)
    case 'blocked':
      return 'text'
    default:
      return 'none'
  }
}

function asApi(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, String(e))
}

function AnswerCard({ wait: w, now, fieldRef, onFieldEscape, ended }: AnswerProps) {
  const m = useAnswerWait()
  // the draft lives here, above the form: an optimistic answer hides the form, a refusal brings it back intact
  const [text, setText] = useState('')
  const [choice, setChoice] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const form = answerForm(w)
  const label = waitLabel(w.type)

  if (w.state === 'answered' && w.answer) {
    return (
      <section className="wait over" aria-label="Réponse envoyée">
        <div className="wait-h">
          {ended ? 'Réponse envoyée · session terminée, ta réponse ira à sa prochaine session' : 'Réponse envoyée · la session reprend'}
          <span className="kind">{label}</span>
        </div>
        <div className="wait-b">
          {w.reason ? <p className="wait-q">{w.reason}</p> : null}
          <p className="wa-sent">
            <span className="wa-sk">Toi · envoyée à {hhmm(w.answer.at)}</span>
            <span>{answerText(w, w.answer)}</span>
          </p>
        </div>
      </section>
    )
  }

  const open = w.state === 'open'
  const answers = open && form !== 'native' && form !== 'items' && form !== 'none'
  const age = ageOf(w.opened_at, now)

  const submit = (req: WaitAnswerRequest) => {
    setFailure(null)
    m.mutateAsync({ id: w.id, req }).then(
      () => {
        setText('')
        setChoice(null)
      },
      (e: unknown) => setFailure(answerFailed(asApi(e))),
    )
  }
  const send = () => {
    const t = text.trim()
    if (form === 'choice' && choice) submit(t ? { choice, reason: t } : { choice })
    else if (t) submit({ text: t })
  }
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      send()
    } else if (e.key === 'Escape' && onFieldEscape) {
      // the page's Esc (close the reader) waits for a second one
      e.preventDefault()
      onFieldEscape()
    }
  }
  const options = w.details?.options ?? []

  return (
    <section className="wait" aria-label="Attend ta réponse">
      <div className="wait-h">
        Attend ta réponse
        {age ? <span className="muted">· depuis {age}</span> : null}
        <span className="kind">{label}</span>
      </div>
      <div className="wait-b">
        {w.reason ? <p className="wait-q">{w.reason}</p> : null}
        {form === 'native' && w.details?.cmd ? <code className="wa-cmd">{w.details.cmd}</code> : null}
        {form === 'native' && w.details?.where ? <p className="wa-where">{w.details.where}</p> : null}
        {form === 'items' ? (
          <ol className="wa-items">
            {(w.details?.items ?? []).map((it, i) => (
              <li key={i}>{it.label}</li>
            ))}
          </ol>
        ) : null}
        {answers && form === 'choice' && options.length ? (
          <div className="wa-opts" role="radiogroup" aria-label="Options">
            {options.map((o) => (
              <button
                key={o.id}
                type="button"
                role="radio"
                className="wa-o"
                aria-checked={choice === o.id}
                onClick={() => setChoice((c) => (c === o.id ? null : o.id))}
              >
                <span className="rad" aria-hidden="true" />
                <span className="wa-ol">
                  <b>{o.id}</b> · {o.label}
                </span>
                {o.reco ? <span className="gm-tag">reco</span> : null}
              </button>
            ))}
          </div>
        ) : null}
      </div>
      {answers ? (
        <div className="wait-f">
          <textarea
            ref={fieldRef}
            className="wa-in"
            aria-label="Ta réponse"
            placeholder={form === 'choice' ? "Ta réponse, ou une note sur l'option choisie…" : 'Ta réponse…'}
            value={text}
            rows={2}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
          />
          {failure ? (
            <p className="wa-fail" role="alert">
              {failure}
            </p>
          ) : null}
          <div className="wa-fr">
            {onFieldEscape ? (
              <span className="ql-hint">
                <kbd>Esc</kbd> replier
              </span>
            ) : null}
            <span className="c-sp" />
            {form === 'seen' ? (
              <button type="button" className="btnl" onClick={() => submit(text.trim() ? { seen: true, reason: text.trim() } : { seen: true })}>
                Vu
              </button>
            ) : null}
            <button type="button" className="btnp" aria-keyshortcuts="Meta+Enter" onClick={send}>
              Envoyer <span className="k">⌘↩</span>
            </button>
          </div>
        </div>
      ) : null}
    </section>
  )
}
