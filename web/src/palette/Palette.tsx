import { Command } from 'cmdk'
import { useState, type KeyboardEvent } from 'react'
import type { Theme } from '@/api/types'
import type { Row } from '@/domain/group'
import { fold } from '@/domain/search'
import type { ListModel } from '@/list/model'
import { Icon, Kbd, StatusIcon } from '@/list/StatusIcon'
import { areaProject } from '@/panel/Breadcrumb'
import { getUi, setUi, useUi } from '@/state/ui'
import { CaptureMode } from './CaptureMode'
import { useAppearance } from '@/theme/appearance'
import { commands, type ListedCommand, type PaletteCtx, type SubjectActions } from './registry'

/** The theme commands sit under their own headings (t3: Thème, Apparence, Réglages); every other command shares one group, headed « Commandes » while searching. */
const THEME_HEADINGS: Record<string, string> = { theme: 'Thème', appearance: 'Apparence', settings: 'Réglages' }

function groupsOf(cmds: ListedCommand[]): { key: string; heading?: string; cmds: ListedCommand[] }[] {
  const rest = cmds.filter((c) => !THEME_HEADINGS[c.group])
  return [
    ...(rest.length ? [{ key: 'commands', cmds: rest }] : []),
    ...Object.entries(THEME_HEADINGS).flatMap(([key, heading]) => {
      const own = cmds.filter((c) => c.group === key)
      return own.length ? [{ key, heading, cmds: own }] : []
    }),
  ]
}

/** Subjects listed under « Sujets » at most (spec § Palette). */
export const PALETTE_SUBJECTS = 6

/** Closes the palette; the selected row takes the focus back. */
export function closePalette() {
  setUi((s) => ({ palette: false, focusTick: s.focusTick + 1 }))
}

/** True when every word of the query is in the text, accents and case ignored (t10 hasAll). */
function hasAll(words: string[], text: string): boolean {
  const t = fold(text)
  return words.every((w) => t.includes(w))
}

/** The capture's side of the palette: the projects, the current one (the selected line's) and the save (the Shell's useCapture). */
export interface CaptureProps {
  themes: readonly Theme[]
  current?: string
  onSave: (themeId: string, text: string) => void
}

/** ⌘K: its commands, or its « Capture › » mode (c, « Capturer… », the phone's « + »). */
export function Palette({
  model,
  ctx,
  onPick,
  capture,
  actions,
}: {
  model: ListModel
  ctx: PaletteCtx
  onPick: (id: string) => void
  capture: CaptureProps
  actions?: SubjectActions
}) {
  const mode = useUi((s) => s.palette)
  const view = useUi((s) => s.view)
  if (mode === 'capture') return <CaptureMode themes={capture.themes} current={capture.current} phone={ctx.phone} onSave={capture.onSave} />
  // the selected subject's commands (D15) read the selection at opening, as the list shows it
  const sel = getUi().sel
  // only where the selection is on screen and writable: the read-only Sessions view keeps a cached backlog selection nobody sees
  const row = sel && view === 'backlog' && model.visible.some((r) => r.s.id === sel) ? model.base.find((r) => r.s.id === sel) : undefined
  const full: PaletteCtx = row ? { ...ctx, selected: row.s, display: row.d, actions } : ctx
  return <Commands model={model} ctx={full} onPick={onPick} />
}

/** The commands, then with a query the subjects of the active scopes; picking a subject goes to it (onPick). */
function Commands({ model, ctx, onPick }: { model: ListModel; ctx: PaletteCtx; onPick: (id: string) => void }) {
  const { resolved } = useAppearance()
  const [q, setQ] = useState('')
  const words = fold(q).trim().split(/\s+/).filter(Boolean)
  const all = commands(ctx)
  const cmds = words.length ? all.filter((c) => hasAll(words, `${c.label} ${c.keywords ?? ''}`)) : all
  const subs: Row[] = words.length ? model.base.filter((r) => hasAll(words, `${r.short} ${r.s.title}`)).slice(0, PALETTE_SUBJECTS) : []
  // ⌘1…⌘9 goes to the Nth subject found; e.code, so the layout (AZERTY's « & » on 1) does not matter
  const onKeyDown = (e: KeyboardEvent) => {
    const n = e.metaKey && !e.altKey && !e.ctrlKey ? /^Digit([1-9])$/.exec(e.code) : null
    const r = n ? subs[Number(n[1]) - 1] : undefined
    if (!r) return
    e.preventDefault()
    onPick(r.s.id)
  }
  return (
    <>
      <div className="scrim" onClick={closePalette} />
      <div className="palette" role="dialog" aria-modal="true" aria-label="Palette de commandes" onKeyDown={onKeyDown}>
        <Command shouldFilter={false} loop label="Palette de commandes">
          <div className="pal-in">
            <Icon name="search" />
            <Command.Input className="pal-input" autoFocus value={q} onValueChange={setQ} placeholder="Aller à un sujet, filtrer, grouper…" />
          </div>
          <Command.List className="pal-list" aria-label="Résultats">
            {groupsOf(cmds).map((g) => (
              <Command.Group key={g.key} heading={g.heading ?? (words.length ? 'Commandes' : undefined)}>
                {g.cmds.map((c) => (
                  <Command.Item
                    key={c.id}
                    value={`cmd:${c.id}`}
                    className="pal-i"
                    onSelect={() => {
                      // closed first: a command may open another layer (« Capturer… » opens the capture mode)
                      closePalette()
                      c.run()
                    }}
                  >
                    <span className="pal-l">
                      {c.icon ? 'glyph' in c.icon ? <Icon name={c.icon.glyph} /> : <span className="thsw" aria-hidden="true" data-theme-palette={c.icon.theme} data-theme={resolved}><i /><i /><b /></span> : null}
                      <span className="pal-t">
                        {c.lead && c.label.startsWith(c.lead) ? (
                          <>
                            <b>{c.lead}</b>
                            {c.label.slice(c.lead.length)}
                          </>
                        ) : (
                          c.label
                        )}
                      </span>
                      {c.hint ? <span className="muted">{c.hint}</span> : null}
                    </span>
                    {c.badge ? (
                      <span className="pal-cur">
                        <Icon name="check" />
                        {c.badge}
                      </span>
                    ) : null}
                    {c.keys ? (
                      <span className="pal-keys">
                        <Kbd k={c.keys} />
                      </span>
                    ) : null}
                  </Command.Item>
                ))}
              </Command.Group>
            ))}
            {subs.length ? (
              <Command.Group heading="Sujets">
                {subs.map((r, i) => (
                  <Command.Item key={r.s.id} value={`sub:${r.s.id}`} className="pal-i" aria-keyshortcuts={`Meta+${i + 1}`} onSelect={() => onPick(r.s.id)}>
                    <span className="pal-l">
                      <StatusIcon d={r.d} />
                      <span className="pal-t">{r.short}</span>
                      <span className="muted">{areaProject(r)}</span>
                    </span>
                    <span className="pal-keys">
                      <Kbd k={`⌘${i + 1}`} />
                    </span>
                  </Command.Item>
                ))}
              </Command.Group>
            ) : null}
            {!cmds.length && !subs.length ? (
              <p className="pal-i pal-none">
                <span className="muted">Aucun résultat pour « {q.trim()} »</span>
              </p>
            ) : null}
          </Command.List>
          <p className="pal-hint">
            <span className="kb">
              <Kbd k="↑ ↓" />
              <span>choisir</span>
            </span>
            <span className="kb">
              <Kbd k="⏎" />
              <span>exécuter</span>
            </span>
            <span className="kb">
              <Kbd k="esc" />
              <span>fermer</span>
            </span>
          </p>
        </Command>
      </div>
    </>
  )
}
