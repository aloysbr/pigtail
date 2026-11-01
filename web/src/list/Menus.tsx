import { useRef } from 'react'
import { GROUP_LABEL, GROUP_MENU, GROUPINGS, type Grouping } from '@/domain/group'
import { LABEL, type Display } from '@/domain/status'
import { setUi, useUi } from '@/state/ui'
import { useMenuAnchor } from './menuAnchor'
import type { AreaItem, ListModel, ProjectItem } from './model'
import { Icon, Kbd, nf, StatusIcon } from './StatusIcon'

/** The statuses the filter offers (Abandonné has its own toggle). */
export const FILTER_STATUSES: readonly Display[] = ['cours', 'ouvert', 'standby', 'done']

/** Changes the grouping; folds and « more » start again from the defaults of the new grouping. */
export function pickGrouping(g: Grouping) {
  setUi({ group: g, menu: null, open: {}, more: {} })
}

/** Filters on an area (single choice), or clears it when it is the current one; the project filter gives way, as the area is one place. From the Sessions view the click only brings the backlog back on that area (its filter is not on show there, so no toggle-off). */
export function pickArea(a: AreaItem) {
  setUi((s) => ({
    areaF: s.view !== 'sessions' && s.areaF && s.areaF.scope === a.scope && s.areaF.name === a.name ? null : { scope: a.scope, name: a.name },
    projectF: null,
    menu: null,
    floatOpen: false,
    view: 'backlog',
    sessionPanel: false,
  }))
}

/** Filters on a project (single choice), or clears it when it is the current one; the area filter gives way, as the project is one place. From the Sessions view the click only brings the backlog back on that project (no toggle-off). */
export function pickProject(p: ProjectItem) {
  setUi((s) => ({ projectF: s.view !== 'sessions' && s.projectF === p.key ? null : p.key, areaF: null, menu: null, floatOpen: false, view: 'backlog', sessionPanel: false }))
}

/** Filters on a status (single choice), or clears it when it is the current one. */
export function pickStatus(d: Display) {
  setUi((s) => ({ statusF: s.statusF === d ? null : d, menu: null }))
}

/** The digit of a key, read from its code so AZERTY works (« é » is Digit2). */
export function digitOf(code: string): number | null {
  const m = /^(?:Digit|Numpad)([1-9])$/.exec(code)
  return m ? Number(m[1]) : null
}

/** « Grouper : … » and its menu (v, then 1-4). */
export function GroupMenu({ icon = false }: { icon?: boolean }) {
  const group = useUi((s) => s.group)
  const open = useUi((s) => s.menu === 'group')
  const menu = useRef<HTMLDivElement>(null)
  useMenuAnchor(menu, open, 'right')
  return (
    <div className="gmenu-w">
      <button
        type="button"
        className="ghost sm gtrig"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={icon ? `Grouper : ${GROUP_LABEL[group]}` : undefined}
        title={icon ? `Grouper : ${GROUP_LABEL[group]}` : undefined}
        onClick={() => setUi({ menu: open ? null : 'group' })}
      >
        {icon ? (
          <Icon name="group" />
        ) : (
          <>
            Grouper : <b>{GROUP_LABEL[group]}</b>
          </>
        )}
        <Kbd k="v" />
        <Icon name="chev" />
      </button>
      {open ? (
        <div ref={menu} className="gmenu" role="menu" aria-label="Grouper par">
          {GROUPINGS.map((g, i) => (
            <button key={g} type="button" role="menuitemradio" aria-checked={g === group} onClick={() => pickGrouping(g)}>
              <span>{GROUP_MENU[g]}</span>
              <kbd>{i + 1}</kbd>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}

/** « + Filtre » and its menu: areas (1-9) then statuses. */
export function FilterMenu({ model }: { model: ListModel }) {
  const open = useUi((s) => s.menu === 'filter')
  const areaF = useUi((s) => s.areaF)
  const statusF = useUi((s) => s.statusF)
  const menu = useRef<HTMLDivElement>(null)
  useMenuAnchor(menu, open, 'left')
  return (
    <div className="gmenu-w">
      <button
        type="button"
        className="fadd"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setUi({ menu: open ? null : 'filter' })}
      >
        <Icon name="plus" />
        Filtre
        <Kbd k="f" />
      </button>
      {open ? (
        <div ref={menu} className="gmenu left" role="menu" aria-label="Filtrer">
          <div className="gm-sec" role="presentation">
            Aire
          </div>
          {model.areas.map((a, i) => (
            <button
              key={`${a.scope}/${a.name}`}
              type="button"
              role="menuitemradio"
              aria-checked={!!areaF && areaF.scope === a.scope && areaF.name === a.name}
              onClick={() => pickArea(a)}
            >
              <span>
                {a.name}
                {model.active.length > 1 ? <span className="muted"> · {a.scope}</span> : null}
              </span>
              <span className="pal-keys">
                <span className="pill-n gm-n">{nf(a.count)}</span>
                <span className="gm-k">{i < 9 ? <kbd>{i + 1}</kbd> : null}</span>
              </span>
            </button>
          ))}
          <div className="gm-sec" role="presentation">
            Statut
          </div>
          {FILTER_STATUSES.map((d) => (
            <button key={d} type="button" role="menuitemradio" aria-checked={statusF === d} onClick={() => pickStatus(d)}>
              <span className="pal-l">
                <StatusIcon d={d} />
                {LABEL[d]}
              </span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}
