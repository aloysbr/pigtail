import type { ReactNode } from 'react'
import { Icon } from '@/list/StatusIcon'

/**
 * A bottom sheet (t10 .msheet): a modal dialog at the foot of a phone screen over a scrim, with its title on the left
 * and « Fermer » on the right. The scrim and « Fermer » call onClose; Esc is the shell's (closeLayer).
 */
export function Sheet({ label, title, onClose, children }: { label: string; title: ReactNode; onClose: () => void; children: ReactNode }) {
  return (
    <>
      <div className="scrim" onClick={onClose} />
      <div className="msheet" role="dialog" aria-modal="true" aria-label={label}>
        <div className="ms-h">
          {title}
          <span className="c-sp" />
          <button type="button" className="ib ms-x" aria-label="Fermer" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        {children}
      </div>
    </>
  )
}
