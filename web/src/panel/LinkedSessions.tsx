import { usePeersAll } from '@/api/sessions'
import { byGroup, visible } from '@/domain/sessions'
import { SessionRow } from '@/sessions/SessionRow'
import { setUi } from '@/state/ui'

/** A linked line: that session's panel, in the Sessions view (one panel at a time: the subject sheet closes). */
function openLinked(id: string) {
  setUi((s) => ({ view: 'sessions', sessionSel: id, sessionPanel: true, panel: false, menu: null, focusTick: s.focusTick + 1 }))
}

/**
 * « Sessions liées · N » (t3 app.js:252): the visible sessions of the subject, compact, waiting first, then working, then
 * resting. The /peers?all=1 read is the Sessions view's (same cache key), mounted only while a sheet is open. No linked
 * session, or the read not there yet: no section at all.
 */
export function LinkedSessions({ subjectId, now }: { subjectId: string; now: number }) {
  const q = usePeersAll()
  const mine = byGroup((q.data ?? []).filter((p) => p.subject_id === subjectId && visible(p)))
  if (!mine.length) return null
  return (
    <section className="lp-sec" aria-labelledby="ls-h">
      <h3 className="lp-h3" id="ls-h">
        Sessions liées · {mine.length}
      </h3>
      <ul className="srows ls-rows">
        {mine.map((p) => (
          <SessionRow key={p.session_id} p={p} now={now} compact onOpen={openLinked} />
        ))}
      </ul>
    </section>
  )
}
