import { useRef } from 'react'
import { setUi, toggleScope, useUi } from '@/state/ui'
import { useMenuAnchor } from './menuAnchor'
import type { ListModel } from './model'
import { Icon, nf } from './StatusIcon'

/** One toggle pill per scope (aria-pressed); the last one on cannot be switched off. */
export function ScopePills({ model }: { model: ListModel }) {
  const lastOn = model.active.length <= 1
  return (
    <div className="pills" role="group" aria-label="Scopes affichés">
      {model.scopes.map((sc) => {
        const on = model.active.includes(sc)
        const locked = on && lastOn
        return (
          <button
            key={sc}
            type="button"
            className="pill"
            aria-pressed={on}
            aria-disabled={locked ? true : undefined}
            title={locked ? 'Au moins un scope reste affiché' : undefined}
            onClick={() => toggleScope(sc, model.scopes)}
          >
            {on ? (
              <span className="pill-c">
                <Icon name="check" />
              </span>
            ) : null}
            {sc}
            <span className="pill-n">{nf(model.scopeCounts[sc] ?? 0)}</span>
          </button>
        )
      })}
    </div>
  )
}

/** The scopes folded into one button (header level 1 and 2): « Scopes · on/all » and its menu. */
export function ScopeMenu({ model }: { model: ListModel }) {
  const open = useUi((s) => s.menu === 'scopes')
  const lastOn = model.active.length <= 1
  const menu = useRef<HTMLDivElement>(null)
  useMenuAnchor(menu, open, 'left')
  return (
    <div className="gmenu-w">
      <button
        type="button"
        className="ghost sm"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setUi({ menu: open ? null : 'scopes' })}
      >
        <span className="gm-l">
          Scopes · {model.active.length}/{model.scopes.length}
        </span>
      </button>
      {open ? (
        <div ref={menu} className="gmenu left" role="menu" aria-label="Scopes affichés">
          {model.scopes.map((sc) => {
            const on = model.active.includes(sc)
            const locked = on && lastOn
            return (
              <button
                key={sc}
                type="button"
                role="menuitemcheckbox"
                aria-checked={on}
                aria-disabled={locked ? true : undefined}
                title={locked ? 'Au moins un scope reste affiché' : undefined}
                onClick={() => toggleScope(sc, model.scopes)}
              >
                <span>{sc}</span>
                <span className="pill-n">{nf(model.scopeCounts[sc] ?? 0)}</span>
              </button>
            )
          })}
        </div>
      ) : null}
    </div>
  )
}
