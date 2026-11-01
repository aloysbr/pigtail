import { useQueryClient } from '@tanstack/react-query'
import { useState, type KeyboardEvent } from 'react'
import { ApiError } from '@/api/client'
import { useSend, useWrite } from '@/api/mutate'
import { COPY, refusalToast } from '@/api/refusals'
import type { Capture, Peer, Space, Subject, Theme } from '@/api/types'
import { NO_AREA } from '@/domain/group'
import { CaptureIcon } from '@/list/CapturesRow'
import { buildModel, revealCapture } from '@/list/model'
import { Icon, Kbd } from '@/list/StatusIcon'
import { getUi, openCapture, setUi, type UiState } from '@/state/ui'
import { Sheet } from '@/ui/Sheet'
import { pushToast } from '@/ui/toasts'
import { proposeProjects, whyOf } from './projects'
import { registerCommand } from './registry'

/** The copies of a capture (t10 « Capturée dans X », D9-10 « Capture supprimée »). */
export const CAPTURE_COPY = {
  saved: (project: string) => `Capturée dans ${project}`,
  removed: 'Capture supprimée',
}

/** « Capturer… » in the palette's commands (c): opens the « Capture › » mode. */
export function registerCapture() {
  registerCommand({ id: 'capture', label: 'Capturer…', group: 'actions', keys: 'c', run: () => openCapture() })
}

/** Closes the palette (or the sheet); the selected row takes the focus back. */
function close() {
  setUi((s) => ({ palette: false, focusTick: s.focusTick + 1 }))
}

let seq = 0

interface CreateVars {
  text: string
  tmp: Capture
}

/**
 * ⏎ in « Capture › » (D5): `POST /captures` `{theme_id, text}` — the pigtail capture alone, no vault line. The text is
 * trimmed, a blank one writes nothing. Optimistic: the capture shows at once, selected under its project's unfolded
 * « Captures · n » line (the grouping goes back to Aire when it is Statut or Scope, the filters that would hide it clear);
 * a refusal puts the list and the selection back with its toast. Success: « Capturée dans X » with its undo, `DELETE
 * /captures/{id}`, valid while the capture is still listed. Made once by the Shell (it outlives the palette).
 */
export function useCapture(): (themeId: string, text: string) => void {
  const qc = useQueryClient()
  const send = useSend()
  const create = useWrite<CreateVars, Capture>({
    mutationFn: ({ tmp, text }) => send<Capture>('POST', '/captures', { theme_id: tmp.theme_id, text }),
    optimistic: ({ tmp }) => {
      const prev = qc.getQueryData<Capture[]>(['captures'])
      qc.setQueryData<Capture[]>(['captures'], [...(prev ?? []), tmp])
      return () => qc.setQueryData(['captures'], prev)
    },
    invalidate: [['captures']],
  })
  const remove = useWrite<string, unknown>({
    mutationFn: (id) => send('DELETE', `/captures/${encodeURIComponent(id)}`),
    optimistic: (id) => {
      const prev = qc.getQueryData<Capture[]>(['captures'])
      if (prev) qc.setQueryData<Capture[]>(['captures'], prev.filter((c) => c.id !== id))
      return () => qc.setQueryData(['captures'], prev)
    },
    invalidate: [['captures']],
  })

  /** Shows a capture: the grouping and filters that would hide it give way, its path and Captures line unfold, it is selected. */
  const reveal = (tmp: Capture, theme: Theme) => {
    const ui = getUi()
    const patch: Partial<UiState> = { palette: false, panel: false, menu: null }
    if (ui.group === 'statut' || ui.group === 'scope') patch.group = 'aire'
    if (ui.statusF) patch.statusF = null
    if (ui.areaF && (ui.areaF.scope !== tmp.scope || ui.areaF.name !== (theme.space || NO_AREA))) patch.areaF = null
    if (ui.query.trim()) patch.query = ''
    // a scope switched off is switched back on for this view (not remembered: a reload keeps the stored choice)
    if (ui.scopesOff.includes(tmp.scope)) patch.scopesOff = ui.scopesOff.filter((x) => x !== tmp.scope)
    const data = {
      subjects: qc.getQueryData<Subject[]>(['subjects']) ?? [],
      themes: qc.getQueryData<Theme[]>(['themes']) ?? [],
      spaces: qc.getQueryData<Space[]>(['spaces']) ?? [],
      peers: qc.getQueryData<Peer[]>(['peers']) ?? [],
      captures: [...(qc.getQueryData<Capture[]>(['captures']) ?? []), tmp],
    }
    const model = buildModel(data, { ...ui, ...patch })
    const open = revealCapture(model.groups, theme.id)
    setUi((s) => ({ ...patch, open: { ...s.open, ...open }, sel: tmp.id, focusTick: s.focusTick + 1 }))
  }

  return (themeId, raw) => {
    const text = raw.trim()
    if (!text) return
    const theme = (qc.getQueryData<Theme[]>(['themes']) ?? []).find((t) => t.id === themeId)
    if (!theme) return
    seq += 1
    const tmp: Capture = { id: `cap-pending-${seq}`, theme_id: themeId, text, scope: theme.scope ?? '', created_at: new Date().toISOString() }
    const before = getUi().sel
    reveal(tmp, theme)
    void (async () => {
      let made: Capture | undefined
      try {
        made = await create.mutateAsync({ text, tmp })
      } catch (e) {
        if (getUi().sel === tmp.id) setUi({ sel: before })
        const err = e instanceof ApiError ? e : new ApiError(0, String(e))
        pushToast({ text: refusalToast(err, { title: theme.name, scope: theme.scope, action: 'capture' }) })
        return
      }
      const real = made?.id ? made : undefined
      if (real) {
        qc.setQueryData<Capture[]>(['captures'], (l) => (l ?? []).map((c) => (c.id === tmp.id ? real : c)))
        if (getUi().sel === tmp.id) setUi((s) => ({ sel: real.id, focusTick: s.focusTick + 1 }))
      }
      pushToast({
        text: CAPTURE_COPY.saved(theme.name),
        undo: real
          ? {
              title: theme.name,
              label: CAPTURE_COPY.removed,
              run: async () => {
                await remove.mutateAsync(real.id)
              },
              stillValid: () => (qc.getQueryData<Capture[]>(['captures']) ?? []).some((c) => c.id === real.id),
            }
          : undefined,
      })
    })()
  }
}

function areaScope(t: Theme): string {
  return `${t.space || NO_AREA} · ${t.scope ?? ''}`
}

/**
 * The « Capture › » mode (spec § Capturer, t10): the field « Une idée, un besoin, en une ligne », the « Projet » list
 * (proposeProjects), ⏎ saves into the chosen project, ⇥ proposes another, ↑↓ choose, ⌫ on an empty field goes back to
 * the commands, Esc closes (the shell's). On the phone: a bottom sheet with project chips and « Enregistrer dans X ».
 */
export function CaptureMode({ themes, current, phone, onSave }: { themes: readonly Theme[]; current?: string; phone: boolean; onSave: (themeId: string, text: string) => void }) {
  const [text, setText] = useState('')
  const [pick, setPick] = useState<string | null>(null)
  const ctx = { named: text, current }
  const ps = proposeProjects(themes, ctx)
  const i = Math.max(0, ps.findIndex((t) => t.id === pick))
  const cur = ps[i] as Theme | undefined
  const why = cur ? whyOf(cur, ctx) : ''
  const save = () => {
    if (!text.trim() || !cur) return
    onSave(cur.id, text)
  }
  const step = (by: number) => {
    if (ps.length) setPick(ps[(i + by + ps.length) % ps.length].id)
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement | HTMLTextAreaElement>) => {
    switch (e.key) {
      case 'Enter':
        e.preventDefault()
        save()
        return
      case 'Tab':
        if (phone) return
        e.preventDefault()
        step(e.shiftKey ? -1 : 1)
        return
      case 'ArrowDown':
      case 'ArrowUp':
        if (phone) return
        e.preventDefault()
        step(e.key === 'ArrowDown' ? 1 : -1)
        return
      case 'Backspace':
        if (text) return
        e.preventDefault()
        setUi({ palette: true })
        return
      default:
    }
  }

  if (phone) {
    return (
      <Sheet label="Capture" title={<span className="pfx">Capture ›</span>} onClose={close}>
        <textarea
          className="ms-in"
          rows={2}
          autoFocus
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
          placeholder="Une idée, un besoin, en une ligne"
          aria-label="Texte de la capture"
        />
        <p className="lp-h3" id="mcap-p">
          Projet
        </p>
        {ps.length ? (
          <div className="pchips" role="group" aria-labelledby="mcap-p">
            {ps.map((t, k) => (
              <button key={t.id} type="button" className="pill" aria-pressed={k === i} onClick={() => setPick(t.id)}>
                <span className="pill-c" aria-hidden="true">
                  {k === i ? <Icon name="check" /> : null}
                </span>
                <span>{t.name}</span>
              </button>
            ))}
          </div>
        ) : (
          <p className="muted">{COPY.noProjects}</p>
        )}
        {cur && why ? (
          <p className="muted ms-why">
            {cur.name} : {why}
          </p>
        ) : null}
        <button type="button" className="mbtn primary" disabled={!text.trim() || !cur} onClick={save}>
          Enregistrer dans {cur?.name ?? ''}
        </button>
      </Sheet>
    )
  }

  return (
    <>
      <div className="scrim" onClick={close} />
      <div className="palette" role="dialog" aria-modal="true" aria-label="Capture">
        <div className="pal-in">
          <span className="pfx">Capture ›</span>
          <input
            className="pal-input"
            role="combobox"
            aria-expanded="true"
            aria-controls="cap-lb"
            aria-activedescendant={cur ? `cap-o${i}` : undefined}
            autoFocus
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            placeholder="Une idée, un besoin, en une ligne"
            aria-label="Texte de la capture"
          />
        </div>
        <p className="pal-sec" id="cap-lbl">
          Projet
        </p>
        {ps.length ? (
          <ul className="pal-list cap-list" role="listbox" id="cap-lb" aria-labelledby="cap-lbl">
            {ps.map((t, k) => {
              const w = whyOf(t, ctx)
              return (
                <li
                  key={t.id}
                  role="option"
                  id={`cap-o${k}`}
                  aria-selected={k === i}
                  className="pal-i"
                  // the field keeps the focus: a click picks the project, the next ⏎ saves into it
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => setPick(t.id)}
                >
                  <span className="pal-l">
                    <CaptureIcon />
                    <span className="pal-n">{t.name}</span>
                    <span className="muted">{areaScope(t)}</span>
                  </span>
                  {w || k === i ? (
                    <span className="pal-r">
                      {w ? <span className="pal-why">{w}</span> : null}
                      {k === i ? (
                        <span className="pal-keys">
                          <Kbd k="⇥" />
                        </span>
                      ) : null}
                    </span>
                  ) : null}
                </li>
              )
            })}
          </ul>
        ) : (
          <p className="pal-empty">{COPY.noProjects}</p>
        )}
        <p className="pal-hint">
          <span className="kb">
            <Kbd k="⏎" />
            <span>enregistrer dans {cur?.name ?? ''}</span>
          </span>
          <span className="kb">
            <Kbd k="⇥" />
            <span>autre projet</span>
          </span>
          <span className="kb">
            <Kbd k="↑ ↓" />
            <span>choisir</span>
          </span>
          <span className="kb">
            <Kbd k="⌫" />
            <span>commandes (ligne vide)</span>
          </span>
          <span className="kb">
            <Kbd k="esc" />
            <span>fermer</span>
          </span>
        </p>
      </div>
    </>
  )
}
