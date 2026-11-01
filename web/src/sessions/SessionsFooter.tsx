import { Kbd } from '@/list/StatusIcon'
import { useUi } from '@/state/ui'

function Item({ k, label }: { k: string; label: string }) {
  return (
    <span className="kb">
      <Kbd k={k} />
      {label}
    </span>
  )
}

/** The Sessions view's shortcut bar (t3 KB_SESS, the keys this view really has): no backlog key, no gesture /peers cannot back. Hidden with the backlog's (« , » there). */
export function SessionsFooter() {
  const hidden = useUi((s) => s.kbHidden)
  if (hidden) return null
  return (
    <footer className="kbdbar" role="contentinfo" aria-label="Raccourcis clavier">
      <div className="kb-items">
        <Item k="↑ ↓" label="Naviguer" />
        <Item k="⏎" label="Détail" />
        <Item k="/" label="Filtrer" />
        <Item k="⌘K" label="Palette" />
      </div>
    </footer>
  )
}
