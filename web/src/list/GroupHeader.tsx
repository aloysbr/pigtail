import type { Group } from '@/domain/group'
import { LABEL, ORDER, type Display } from '@/domain/status'
import { setUi } from '@/state/ui'
import { Pie } from '@/ui/Pie'
import { Icon, nf, StatusIcon } from './StatusIcon'

/** Folds or unfolds a group or a status line. */
export function toggleOpen(key: string, open: boolean) {
  setUi((s) => ({ open: { ...s.open, [key]: !open } }))
}

/** The status summary of a header: one icon + count per displayed status present. */
export function Summary({ c }: { c: Partial<Record<Display, number>> }) {
  return (
    <span className="gsum">
      {ORDER.filter((k) => c[k]).map((k) => (
        <span className="gs" key={k}>
          <StatusIcon d={k} />
          <span>{nf(c[k] as number)}</span>
          <span className="sr">{LABEL[k]}</span>
        </span>
      ))}
    </span>
  )
}

/** A foldable group header (h2 at level 1, h3 at level 2): name, kind, count, status summary. */
export function GroupHeader({ g, open }: { g: Group; open: boolean }) {
  const H = g.level === 1 ? 'h2' : 'h3'
  return (
    <H className={`gh g${g.level}`}>
      <button type="button" className="gbtn" aria-expanded={open} data-g={g.key} onClick={() => toggleOpen(g.key, open)}>
        <Icon name="chev" className="gchev" />
        {g.kind === 'status' ? <StatusIcon d={g.key.slice(3) as Display} /> : null}
        {g.kind === 'project' ? <Pie className="gh-pie" done={g.pie?.done ?? 0} total={g.pie?.total ?? 0} /> : null}
        <span className="gname">{g.label}</span>
        {g.sub ? <span className="gsub">{g.sub}</span> : null}
        <span className="lg-n" data-testid="count">
          {nf(g.count)}
        </span>
        <span className="c-sp" />
        {g.kind === 'status' ? null : <Summary c={g.summary} />}
      </button>
    </H>
  )
}
