import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import type { Row } from '@/domain/group'
import { phaseOf, PHASES, statusLabel, statusTitle } from '@/domain/status'
import { canToggleStandby } from '@/domain/standby'
import { canLaunch, useLaunch, useLaunching } from '@/list/launch'
import { Icon, Kbd, StatusIcon } from '@/list/StatusIcon'
import { useToggleStandby } from '@/list/status'
import { closePanel } from '@/state/ui'
import { LaunchHereButton, SdkSessionBlock } from '@/sdk/SdkSessionBlock'
import { PendingButton } from '@/ui/PendingButton'
import { areaProject, Breadcrumb } from './Breadcrumb'
import { History } from './History'
import { LinkedSessions } from './LinkedSessions'
import { Links } from './Links'

/** The actions of the sheet: « Lancer l » on an Ouvert or a Standby (« Lancement… » while it runs); empty otherwise (no « Rejoindre », D2). */
export function PanelActions({ row }: { row: Row }) {
  const { s, d } = row
  const launch = useLaunch()
  const busy = useLaunching(s.id)
  return (
    <div className="lp-acts" data-slot="actions" data-subject={s.id}>
      {canLaunch(s, d) || busy ? (
        <PendingButton className="launch-btn" pending={busy} label="Lancer" busy="Lancement…" keys="l" onClick={() => launch(s.id)} />
      ) : null}
      {canLaunch(s, d) ? <LaunchHereButton subjectId={s.id} title={s.short_title || s.title} scope={s.scope} /> : null}
    </div>
  )
}

/** « Cycle de vie » (t10 .cycle, D37): Capture → Ouvert → [En cours | Standby] → Done; the third box reads one or the other and carries the ⏸ / ▶ switch (`h`), never on Done, Abandonné nor a capture (D3). */
function Cycle({ row }: { row: Row }) {
  const toggle = useToggleStandby()
  const { s, d } = row
  const parked = s.status === 'parked'
  // En cours over a stored parked: the box reads Standby and says what is stored (spec D10)
  const masked = d === 'cours' && parked
  const at = s.status === 'captured' ? 'capture' : d === 'cours' || d === 'standby' ? 'mid' : d
  const label = parked ? 'Reprendre' : 'Mettre en standby'
  const node = (key: string, text: string, icon: ReactNode, extra?: ReactNode, title?: string) => (
    <span className={cn('cy', key === 'mid' && 'cy-mid', at === key && 'cur')} aria-current={at === key ? 'step' : undefined} title={title}>
      <span className="cy-i" aria-hidden="true">
        {icon}
      </span>
      <span>{text}</span>
      {extra}
    </span>
  )
  const arrow = (
    <span className="cy-a" aria-hidden="true">
      <svg viewBox="0 0 16 16">
        <path d="M2.5 8h10M9.5 5l3 3-3 3" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </span>
  )
  const sw = canToggleStandby(s) ? (
    <button type="button" className="cy-sw" aria-label={label} aria-keyshortcuts="h" title={`${label} (h)`} onClick={() => void toggle(s.id)}>
      <svg viewBox="0 0 16 16" aria-hidden="true">
        {parked ? (
          <path d="M5 3.5v9l7-4.5z" fill="currentColor" />
        ) : (
          <path d="M5.5 4v8M10.5 4v8" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" />
        )}
      </svg>
      <kbd aria-hidden="true">h</kbd>
    </button>
  ) : null
  const position = masked ? 'Standby' : statusLabel(s, d)
  return (
    <section className="lp-sec" aria-labelledby="cy-h">
      <h3 className="lp-h3" id="cy-h">
        Cycle de vie
      </h3>
      <div className="cycle" role="group" aria-label={`Capture, puis Ouvert, puis En cours ou Standby, puis Done. Position : ${position}`}>
        {node('capture', 'Capture', CAPTURE_ICON)}
        {arrow}
        {node('ouvert', 'Ouvert', <StatusIcon d="ouvert" />)}
        {arrow}
        {parked
          ? node('mid', 'Standby', <StatusIcon d="standby" />, sw, masked ? 'Statut stocké : parked' : undefined)
          : node('mid', 'En cours', <StatusIcon d="cours" />, sw)}
        {arrow}
        {node('done', 'Done', <StatusIcon d="done" />)}
      </div>
      {s.status === 'dropped' ? <p className="muted">Abandonné : sorti du cycle, visible derrière « Voir les abandonnés ».</p> : null}
    </section>
  )
}

/** t10 I.cap. */
const CAPTURE_ICON = (
  <svg viewBox="0 0 16 16">
    <path d="M3.5 2.5h6l3 3v8h-9z" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
    <path d="M5.5 8h5M5.5 10.5h3.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
  </svg>
)

/** The phase part of « Statut »: three ticks + name, « pas commencé », or nothing for a closed subject. */
function phasePart(row: Row): ReactNode {
  const p = phaseOf(row.s.status)
  if (p !== null && (row.d === 'cours' || row.d === 'ouvert')) {
    return (
      <span className="step-i">
        <span className="steps3" aria-hidden="true">
          {[0, 1, 2].map((i) => (
            <i key={i} className={i < p ? 'done' : i === p ? 'cur' : ''} />
          ))}
        </span>
        <span>{PHASES[p]}</span>
      </span>
    )
  }
  // t10: a closed subject (Done, Abandonné) says nothing of its phase; a Standby's phase is not known without its log: none invented
  if (row.d === 'done' || row.d === 'dropped' || row.d === 'standby') return null
  return <span className="muted">pas commencé</span>
}

/** The subject sheet (spec § Fiche sujet): trail, title, Statut, Aire › projet, Rang, Liens, Historique. */
export function SubjectPanel({
  row,
  page = false,
  onStep,
  canPrev = true,
  canNext = true,
}: {
  row: Row
  page?: boolean
  onStep?: (by: 1 | -1) => void
  canPrev?: boolean
  canNext?: boolean
}) {
  const { s, d } = row
  const label = statusLabel(s, d)
  const phase = phasePart(row)
  return (
    <section className="lpanel" aria-labelledby="lp-title">
      <div className="lp-h">
        {page ? (
          <button type="button" className="ghost sm" aria-label="Retour à la liste" onClick={closePanel}>
            <Icon name="back" />
            Backlog
            <Kbd k="esc" />
          </button>
        ) : (
          <button type="button" className="ib x-l" aria-label="Fermer le panneau" aria-keyshortcuts="Escape" onClick={closePanel}>
            <Icon name="x" />
          </button>
        )}
        <Breadcrumb row={row} />
        <span className="c-sp" />
        <button type="button" className="ib" aria-label="Sujet précédent" aria-keyshortcuts="k" aria-disabled={!canPrev} onClick={() => canPrev && onStep?.(-1)}>
          <Icon name="back" />
        </button>
        <button type="button" className="ib" aria-label="Sujet suivant" aria-keyshortcuts="j" aria-disabled={!canNext} onClick={() => canNext && onStep?.(1)}>
          <Icon name="fwd" />
        </button>
      </div>
      <div className="lp-ttl">
        <StatusIcon d={d} label={label} />
        <h2 className="lp-t" id="lp-title">
          {row.short}
        </h2>
      </div>
      {row.long ? (
        <p className="lp-sub">
          <span className="sr">Titre : </span>
          {row.long}
        </p>
      ) : null}
      <dl className="lp-props">
        <dt>Statut</dt>
        <dd className="st-dd">
          <span className="st-main">
            <StatusIcon d={d} />
            <span title={statusTitle(s)}>{label}</span>
          </span>
          {phase ? (
            <>
              <span className="dsep" aria-hidden="true">
                ·
              </span>
              {phase}
            </>
          ) : null}
        </dd>
        <dt>Aire › projet</dt>
        <dd>
          {areaProject(row)}
          <span className="muted">
            · {row.scope}
            {row.ongoing ? ' · continu' : ''}
          </span>
        </dd>
        <dt>Rang</dt>
        <dd>{s.rank ? `#${s.rank}` : <span className="muted">non classé</span>}</dd>
      </dl>
      <Cycle row={row} />
      <PanelActions row={row} />
      <SdkSessionBlock subjectId={s.id} title={s.short_title || s.title} scope={s.scope} now={Date.now()} />
      <LinkedSessions subjectId={s.id} now={Date.now()} />
      <Links subject={s} />
      <History key={s.id} id={s.id} />
    </section>
  )
}
