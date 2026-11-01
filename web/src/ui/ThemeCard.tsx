import { Icon } from '@/list/StatusIcon'
import { forwardRef } from 'react'
import type { Appearance, PaletteName } from '@/theme/appearance'

/** Names and one-line descriptions of the two themes (t3 THEME_NAMES); the palette command descriptions read them too. */
export const THEME_NAMES: Record<PaletteName, { name: string; desc: string }> = {
  terra: { name: 'Terra', desc: 'Ivoire et terre cuite' },
  ocean: { name: 'Ocean', desc: 'Gris pur et indigo' },
}

/** A theme as a card: a small mock of the web front drawn with the tokens of that theme in the current appearance (the preview carries data-theme-palette and data-theme), its name, its line. */
export const ThemeCard = forwardRef<HTMLButtonElement, { id: PaletteName; on: boolean; resolved: Exclude<Appearance, 'system'>; onPick: () => void }>(function ThemeCard(
  { id, on, resolved, onPick },
  ref,
) {
  const n = THEME_NAMES[id]
  return (
    <button ref={ref} type="button" className="thcard" aria-pressed={on} onClick={onPick}>
      <span className="thprev" aria-hidden="true" data-theme-palette={id} data-theme={resolved}>
        <span className="tp-side">
          <i />
          <i />
          <i />
        </span>
        <span className="tp-main">
          <span className="tp-row">
            <b className="tp-dot" />
            <i className="w1" />
            <span className="tp-chip" />
          </span>
          <span className="tp-row">
            <i className="tp-idle" />
            <i className="w2" />
          </span>
          <span className="tp-row">
            <i className="tp-idle" />
            <i className="w3" />
            <span className="tp-btn" />
          </span>
        </span>
      </span>
      <span className="th-n">
        <b>{n.name}</b>
        {on ? <Icon name="check" className="th-ok" /> : null}
        {on ? <span className="th-on">Actif</span> : null}
      </span>
      <span className="th-d">{n.desc}</span>
    </button>
  )
})
