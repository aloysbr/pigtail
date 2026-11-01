import { useMemo, useState } from 'react'
import { useSubjectLog } from '@/api/log'
import { buildJournal, type Day } from '@/domain/journal'
import { cn } from '@/lib/utils'
import { JournalEvent } from './JournalEvent'

/** Rows (events after folding) shown before « Voir tout » (spec § Fiche sujet). */
export const HISTORY_CAP = 10

/**
 * The journal by day, step and event. The step « en cours » is the step of the newest placed event: no route gives
 * the subject's workflow step, the journal is the only source.
 */
export function journalOf(entries: Parameters<typeof buildJournal>[0], now: Date): Day[] {
  const first = buildJournal(entries, now)
  const current = first[0]?.steps.find((s) => s.id)?.id
  return current ? buildJournal(entries, now, current) : first
}

/** The first `cap` rows, days and steps kept around them; the step spans stay whole. */
function capRows(days: Day[], cap: number): Day[] {
  let left = cap
  const out: Day[] = []
  for (const d of days) {
    if (left <= 0) break
    const steps = []
    for (const s of d.steps) {
      if (left <= 0) break
      const events = s.events.slice(0, left)
      left -= events.length
      steps.push({ ...s, events })
    }
    out.push({ ...d, steps })
  }
  return out
}

/** The day header: « Aujourd'hui » in bold, the date, the count on the right; its name is the whole label. */
function DayHead({ day }: { day: Day }) {
  const parts = day.label.split(' · ')
  const count = parts.pop()
  const today = parts.length > 1 ? parts.shift() : undefined
  return (
    <h4 className="hv-day" aria-label={day.label}>
      {today ? <b>{today}</b> : null}
      <span>{parts.join(' · ')}</span>
      <span className="hv-sp" />
      <span>{count}</span>
    </h4>
  )
}

/** « Historique »: the subject's pigtail log by day, workflow step and typed event (t4), 10 rows then « Voir tout ». Keyed by subject. */
export function History({ id }: { id: string }) {
  const q = useSubjectLog(id)
  const [all, setAll] = useState(false)
  const today = new Date().toDateString() // a new day re-reads « Aujourd'hui » on the next render
  const days = useMemo(() => journalOf(q.data ?? [], new Date()), [q.data, today])
  const rows = days.reduce((n, d) => n + d.steps.reduce((m, s) => m + s.events.length, 0), 0)
  const shown = all ? days : capRows(days, HISTORY_CAP)
  return (
    <section className="lp-sec hv" aria-labelledby="hist-h">
      <h3 className="lp-h3" id="hist-h">
        Historique
      </h3>
      {days.length ? (
        <>
          {shown.map((d) => (
            <div className="hv-dayb" key={d.key}>
              <DayHead day={d} />
              {d.steps.map((s) => (
                <div className={cn('hv-step', s.current && 'cur')} key={s.id || 'none'}>
                  <h5 className="hv-st">
                    <span className="hv-sn">{s.name}</span>
                    {s.rounds ? <span className="hv-sm">{s.rounds}</span> : null}
                    {s.current ? <span className="hv-cur">en cours</span> : null}
                    <span className="hv-rule" aria-hidden="true" />
                    <span className="hv-sr">{s.span}</span>
                  </h5>
                  <ol className="hv-evs">
                    {s.events.map((ev) => (
                      <JournalEvent key={ev.id} ev={ev} />
                    ))}
                  </ol>
                </div>
              ))}
            </div>
          ))}
          {!all && rows > HISTORY_CAP ? (
            <button type="button" className="lnk-b" onClick={() => setAll(true)}>
              Voir tout
            </button>
          ) : null}
        </>
      ) : q.isPending ? (
        <p className="muted">Chargement…</p>
      ) : q.isError ? (
        <p className="muted">Historique indisponible pour l'instant.</p>
      ) : (
        <p className="muted">Aucune entrée dans l'historique.</p>
      )}
    </section>
  )
}
