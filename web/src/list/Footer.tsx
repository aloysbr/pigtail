import { canToggleStandby } from '@/domain/standby'
import { cn } from '@/lib/utils'
import { toggleKeys, useUi } from '@/state/ui'
import { hasUndo, useToasts } from '@/ui/toasts'
import { canLaunch } from './launch'
import type { ListModel } from './model'
import { Kbd } from './StatusIcon'

function Item({ k, label }: { k: string; label: string }) {
  return (
    <span className="kb">
      <Kbd k={k} />
      {label}
    </span>
  )
}

/**
 * The t4 shortcut bar; « , masquer » hides it (remembered), « , raccourcis » brings it back (the comma: no Shift on AZERTY, where
 * ? is Shift+,). It follows the selected line (spec § Pied de raccourcis): l lancer (Ouvert, Standby), h standby / reprendre,
 * ⌥↑ ⌥↓ rang (En cours, Ouvert), c capturer (always), z annuler while an undoable toast shows; nothing for an action the line
 * does not allow.
 *
 * One line at every width: the items are in DOM priority order (the line's actions, then navigation) inside .kb-items, a wrapping
 * box one line high that clips what wraps, whole items only; « , masquer » sits outside it and is always shown. A narrow list
 * (D16) only prints the toggle as its key alone.
 */
export function Footer({ model = null, narrow = false }: { model?: ListModel | null; narrow?: boolean }) {
  const hidden = useUi((s) => s.kbHidden)
  const sel = useUi((s) => s.sel)
  // re-rendered on every toast change: « z annuler » shows while the toast on screen can be undone
  useToasts()
  if (hidden) {
    return (
      <button type="button" className="kbhint" onClick={toggleKeys}>
        <kbd>,</kbd> raccourcis
      </button>
    )
  }
  const onCap = !!sel && !!model?.capIds.has(sel)
  const row = sel && model && !onCap ? model.base.find((r) => r.s.id === sel) : undefined
  return (
    <footer className={cn('kbdbar', narrow && 'kbdbar-n')} role="contentinfo" aria-label="Raccourcis clavier">
      <div className="kb-items">
        {row && canLaunch(row.s, row.d) ? <Item k="l" label="lancer" /> : null}
        {row && canToggleStandby(row.s) ? <Item k="h" label={row.s.status === 'parked' ? 'reprendre' : 'standby'} /> : null}
        {row && (row.d === 'cours' || row.d === 'ouvert') ? <Item k="⌥↑ ⌥↓" label="rang" /> : null}
        <Item k="c" label="capturer" />
        {hasUndo() ? <Item k="z" label="annuler" /> : null}
        <Item k="j k" label="ligne" />
        {/* ⏎ does nothing on a capture (read only, D14) */}
        {onCap ? null : <Item k="⏎" label="ouvrir" />}
        <Item k="← →" label="replier / déplier" />
        {/* D9: the area, grouping and filter keys */}
        <Item k="g 1 … 9" label="aire" />
        <Item k="v" label="grouper" />
        <Item k="f" label="filtrer" />
      </div>
      {/* narrow: the key alone (D16) */}
      <button type="button" className="kb kbhint-in" onClick={toggleKeys} aria-label={narrow ? 'Masquer les raccourcis' : undefined}>
        <Kbd k="," />
        {narrow ? null : 'masquer'}
      </button>
    </footer>
  )
}
