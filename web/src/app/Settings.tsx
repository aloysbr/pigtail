import { useEffect, useRef } from 'react'
import { Icon, Kbd } from '@/list/StatusIcon'
import { useAppearance, type Appearance, type PaletteName } from '@/theme/appearance'
import { setUi } from '@/state/ui'
import { Segment, type SegmentOption } from '@/ui/Segment'
import { ThemeCard } from '@/ui/ThemeCard'

const MODES: readonly SegmentOption<Appearance>[] = [
  { value: 'light', label: 'Clair', icon: 'sun' },
  { value: 'dark', label: 'Sombre', icon: 'moon' },
  { value: 'system', label: 'Système', icon: 'display' },
]
const PALETTES: readonly PaletteName[] = ['terra', 'ocean']

/** Réglages: the theme and the appearance, a 304 px popover over the sidebar's footer button (t3 settings). The choice goes through useAppearance (saveChoice + applyChoice), the same path as the ⌘K commands. */
export function Settings() {
  const { choice, resolved, set } = useAppearance()
  const box = useRef<HTMLDivElement>(null)
  const first = useRef<HTMLButtonElement>(null)

  // the active card takes the focus (it is the choice being edited)
  useEffect(() => {
    first.current?.focus()
  }, [])

  // a press outside closes it; the sidebar button is outside but toggles by itself
  useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (!(e.target instanceof Element)) return
      if (box.current?.contains(e.target) || e.target.closest('.sf-set')) return
      setUi({ settings: false })
    }
    document.addEventListener('pointerdown', onDown)
    return () => document.removeEventListener('pointerdown', onDown)
  }, [])

  return (
    <div ref={box} className="pop settings" role="dialog" aria-label="Réglages">
      <div className="pop-h">
        <b>Réglages</b>
        <span className="c-sp" />
        <button type="button" className="ib" aria-label="Fermer les réglages" aria-keyshortcuts="Escape" title="Fermer (Esc)" onClick={() => setUi({ settings: false })}>
          <Icon name="x" />
        </button>
      </div>
      <section className="pop-sec">
        <h3 className="pop-l" id="st-th">
          Thème
        </h3>
        <div className="thcards" role="group" aria-labelledby="st-th">
          {PALETTES.map((id) => (
            <ThemeCard
              key={id}
              ref={id === choice.palette ? first : undefined}
              id={id}
              on={id === choice.palette}
              resolved={resolved}
              onPick={() => set({ ...choice, palette: id })}
            />
          ))}
        </div>
      </section>
      <section className="pop-sec">
        <h3 className="pop-l" id="st-ap">
          Apparence
        </h3>
        <Segment labelledBy="st-ap" options={MODES} value={choice.appearance} onChange={(appearance) => set({ ...choice, appearance })} />
        <p className="pop-n">{choice.appearance === 'system' ? `Suit le Mac : ${resolved === 'dark' ? 'sombre' : 'clair'} en ce moment.` : 'Système suit le réglage du Mac.'}</p>
      </section>
      <p className="pop-foot">
        Enregistré sur cet appareil · aussi dans <Kbd k="⌘K" /> «&nbsp;thème&nbsp;»
      </p>
    </div>
  )
}
