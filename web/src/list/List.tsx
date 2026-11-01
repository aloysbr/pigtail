import type { Capture } from '@/api/types'
import type { Group } from '@/domain/group'
import { LABEL } from '@/domain/status'
import { cn } from '@/lib/utils'
import { setUi, useUi } from '@/state/ui'
import { GroupHeader, toggleOpen } from './GroupHeader'
import { CapturesRow } from './CapturesRow'
import { bodyOf, isOpen, type ListModel, type Part } from './model'
import type { OnDrop } from './rank'
import { Row } from './Row'
import { Icon, nf, StatusIcon } from './StatusIcon'

function moreLabel(n: number): string {
  return n === 1 ? 'Afficher 1 autre' : `Afficher les ${nf(n)} autres`
}

function Rows({ part, showProject, now, drag }: { part: Extract<Part, { type: 'rows' }>; showProject: boolean; now: number; drag: OnDrop | null }) {
  return (
    <>
      <ul>
        {part.rows.map((r) => (
          <Row key={r.s.id} row={r} showProject={showProject} now={now} drag={drag} />
        ))}
      </ul>
      {part.hidden ? (
        <button type="button" className="more" onClick={() => setUi((s) => ({ more: { ...s.more, [part.key]: true } }))}>
          {moreLabel(part.hidden)}
        </button>
      ) : null}
    </>
  )
}

function Body({ g, showProject, now, drag, caps }: { g: Group; showProject: boolean; now: number; drag: OnDrop | null; caps?: Capture[] }) {
  const open = useUi((s) => s.open)
  const more = useUi((s) => s.more)
  return (
    <>
      {bodyOf(g, { open, more }, caps).map((p) =>
        p.type === 'caps' ? (
          <CapturesRow key={p.key} part={p} now={now} />
        ) : p.type === 'rows' ? (
          <Rows key={p.key} part={p} showProject={showProject} now={now} drag={drag} />
        ) : (
          <div key={p.key} className="sub">
            <button type="button" className="sline" data-g={p.key} aria-expanded={p.open} onClick={() => toggleOpen(p.key, p.open)}>
              <Icon name="chev" className="gchev" />
              <StatusIcon d={p.d} />
              <span>{LABEL[p.d]}</span>
              <span className="lg-n">{nf(p.count)}</span>
            </button>
            {p.part && p.part.type === 'rows' ? <Rows part={p.part} showProject={showProject} now={now} drag={drag} /> : null}
          </div>
        ),
      )}
    </>
  )
}

function Section({ g, model, now, drag }: { g: Group; model: ListModel; now: number; drag: OnDrop | null }) {
  const open = useUi((s) => isOpen(s, g, model.grouping, model.filtering))
  const showProject = model.grouping === 'statut' || model.grouping === 'scope'
  return (
    <section data-group={g.key} className={cn(g.level === 2 && 'sub', g.kind === 'status' && 'by-status')}>
      <GroupHeader g={g} open={open} />
      {open
        ? g.children.length
          ? g.children.map((c) => <Section key={c.key} g={c} model={model} now={now} drag={drag} />)
          : <Body g={g} showProject={showProject} now={now} drag={drag} caps={model.captures[g.key]} />
        : null}
    </section>
  )
}

/** The grouped list (region « Sujets »); `drag`: the drop of a reordering drag (a mouse; null on a phone). */
export function List({ model, now, drag = null }: { model: ListModel; now: number; drag?: OnDrop | null }) {
  return (
    <div className={model.grouping === 'statut' || model.grouping === 'scope' ? 'll6' : 'll6 no-theme'} role="region" aria-label="Sujets">
      {model.groups.map((g) => (
        <Section key={g.key} g={g} model={model} now={now} drag={drag} />
      ))}
    </div>
  )
}
