import { ageOf } from '@/domain/age'
import type { Row as RowData } from '@/domain/group'
import { statusLabel, statusTitle } from '@/domain/status'
import { cn } from '@/lib/utils'
import { openPanel, setUi, useUi } from '@/state/ui'
import { usePhone } from '@/state/viewport'
import { PendingButton } from '@/ui/PendingButton'
import { canLaunch, useLaunch, useLaunching } from './launch'
import { hasPhaseMark, PhaseMark } from './PhaseMark'
import { useRowDrag, type OnDrop } from './rank'
import { StatusIcon } from './StatusIcon'

/**
 * One subject: status · rank · short + long title · phase · project chip · age · Lancer. En cours and Ouvert rows drag with a mouse
 * (`drag`: the drop, null on a phone). The phase and the chip sit in .lr-m: invisible to the wide columns (display: contents), the
 * second line of a narrow row.
 */
export function Row({ row, showProject, now, drag = null }: { row: RowData; showProject: boolean; now: number; drag?: OnDrop | null }) {
  const selected = useUi((s) => s.sel === row.s.id)
  const { s, d } = row
  const dnd = useRowDrag(s.id, d === 'cours' || d === 'ouvert' ? drag : null)
  const rank = s.rank && d === 'ouvert' ? `#${s.rank}` : ''
  return (
    <li
      className={cn('lr6', selected && 'sel', !hasPhaseMark(row) && !showProject && 'no-m', dnd.dragging && 'dragging', dnd.over && 'over')}
      {...dnd.props}
      data-id={s.id}
      tabIndex={selected ? 0 : -1}
      aria-current={selected ? 'true' : undefined}
      onClick={() => openPanel(s.id)}
    >
      <StatusIcon d={d} label={statusLabel(s, d)} title={statusTitle(s)} />
      <span className="rk6">{rank}</span>
      <span className="lr-t min-w-0 truncate">
        <b>{row.short}</b>
        {row.long ? <span className="lr-full">{row.long}</span> : null}
      </span>
      <span className="lr-m">
        <span className="lr-st">
          <PhaseMark stored={s.status} d={d} />
        </span>
        <span className="lr-th">{showProject ? (
            <span className="chip" title={row.projectName}>
              <span className="chip-t">{row.projectName}</span>
            </span>
          ) : null}</span>
      </span>
      <span className="lr-d">{ageOf(s.updated_at, now)}</span>
      <LaunchCell row={row} />
    </li>
  )
}

/** The row's last column: « Lancer l » on an Ouvert or a Standby, « Lancement… » while it runs, empty otherwise (t10 action()). */
function LaunchCell({ row }: { row: RowData }) {
  const launch = useLaunch()
  const busy = useLaunching(row.s.id)
  // the phone launches from the sheet only (t10 .mobile): a row button would cover its rank and age
  const phone = usePhone()
  const id = row.s.id
  return (
    <span className="lr-l">
      {!phone && (canLaunch(row.s, row.d) || busy) ? (
        <PendingButton
          className="launch-btn"
          pending={busy}
          label="Lancer"
          busy="Lancement…"
          keys="l"
          onClick={(e) => {
            // the row's click opens the sheet: the button only selects its row and launches
            e.stopPropagation()
            setUi({ sel: id })
            launch(id)
          }}
        />
      ) : null}
    </span>
  )
}
