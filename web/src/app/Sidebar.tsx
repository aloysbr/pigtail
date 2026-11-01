import { Fragment } from 'react'
import { pickArea, pickProject } from '@/list/Menus'
import type { ListModel } from '@/list/model'
import { Icon, Kbd, nf } from '@/list/StatusIcon'
import { cn } from '@/lib/utils'
import { Pie } from '@/ui/Pie'
import { dockSide, openBacklog, openSessions, setUi, toggleSettings, toggleSide, useUi } from '@/state/ui'

/** The 240 px sidebar: Pigtail, palette, Sessions, Backlog, then each scope shown and its areas. `live` is « N vivantes » (GET /peers), null before its first answer. */
export function Sidebar({ model, float, canDock, live = null }: { model: ListModel | null; float: boolean; canDock: boolean; live?: number | null }) {
  const onSessions = useUi((s) => s.view === 'sessions')
  const areaF = useUi((s) => s.areaF)
  const projectF = useUi((s) => s.projectF)
  const going = useUi((s) => s.menu === 'go')
  const setOn = useUi((s) => s.settings)
  const active = new Set(model?.active ?? [])
  return (
    <aside className={cn('side', float && 'float')} aria-label="Navigation">
      <div className="brand">
        <b>Pigtail</b>
        <span className="brand-r">
          <button
            type="button"
            className="ghost sm"
            aria-label="Palette de commandes et capture (⌘K)"
            aria-keyshortcuts="Meta+K"
            onClick={() => setUi({ palette: true, floatOpen: false })}
          >
            <Icon name="search" />
            <Kbd k="⌘K" />
          </button>
          {float ? (
            canDock ? (
              <button type="button" className="ghost sm" aria-label="Ancrer la barre latérale" onClick={dockSide}>
                Ancrer
              </button>
            ) : null
          ) : (
            <button type="button" className="ghost sm" aria-label="Ranger la barre latérale" aria-keyshortcuts="Meta+B" onClick={() => toggleSide(true)}>
              <Icon name="menu" />
              <Kbd k="⌘B" />
            </button>
          )}
        </span>
      </div>
      <nav className="nav" aria-label="Backlog">
        <button
          type="button"
          className={cn('nav-i', 'nav-sess', onSessions && 'cur')}
          aria-current={onSessions ? 'true' : undefined}
          aria-keyshortcuts="s"
          title="Sessions (s)"
          onClick={openSessions}
        >
          <Icon name="sessions" />
          <span className="nav-l">Sessions</span>{' '}
          {live !== null ? (
            <span className="meta" data-testid="side-live" aria-label={`${live} vivantes`}>
              {nf(live)}
            </span>
          ) : null}
        </button>
        <button
          type="button"
          className={cn('nav-i', !onSessions && !areaF && !projectF && 'cur')}
          aria-current={onSessions || areaF || projectF ? undefined : 'true'}
          onClick={openBacklog}
        >
          <Icon name="list" />
          <span className="nav-l">Backlog</span>{' '}
          {model ? <span className="meta">{nf(model.total)}</span> : null}
        </button>
        {(model?.scopes ?? [])
          .filter((sc) => active.has(sc))
          .map((sc) => (
            <Fragment key={sc}>
              <div className="nav-g">{sc}</div>
              {(model?.areas ?? [])
                .filter((a) => a.scope === sc)
                .map((a) => {
                  // the digit is the area's index in the whole model (f, g), not its index inside this scope
                  const n = (model?.areas ?? []).indexOf(a)
                  const cur = !onSessions && !!areaF && areaF.scope === a.scope && areaF.name === a.name
                  return (
                    <Fragment key={a.name}>
                      <button
                        type="button"
                        className={cn('nav-i', 'nav-area', cur && 'cur')}
                        aria-current={cur ? 'true' : undefined}
                        onClick={() => pickArea(a)}
                      >
                        <span className="nav-l">{a.name}</span>{' '}
                        {/* open (not done) like the project rows below; dropped subjects are already out of the model */}
                        <span className="meta">{nf(a.projects.reduce((n, p) => n + p.open, 0))}</span>
                        {going && n < 9 ? <Kbd k={String(n + 1)} /> : null}
                      </button>
                      {a.projects.map((p) => {
                        const on = !onSessions && projectF === p.key
                        return (
                          <button
                            key={p.key}
                            type="button"
                            className={cn('nav-i', 'nav-proj', on && 'cur')}
                            aria-current={on ? 'true' : undefined}
                            onClick={() => pickProject(p)}
                          >
                            <Pie done={p.done} total={p.total} />{' '}
                            <span className="nav-l">{p.name}</span>{' '}
                            <span className="meta">{nf(p.open)}</span>
                          </button>
                        )
                      })}
                    </Fragment>
                  )
                })}
            </Fragment>
          ))}
      </nav>
      <div className="side-foot">
        <button
          type="button"
          className={cn('ghost', 'sm', 'sf-set', setOn && 'on')}
          aria-haspopup="dialog"
          aria-expanded={setOn}
          aria-keyshortcuts="Meta+,"
          title="Réglages (⌘,)"
          onClick={toggleSettings}
        >
          <Icon name="sliders" />
          Réglages
        </button>
      </div>
    </aside>
  )
}
