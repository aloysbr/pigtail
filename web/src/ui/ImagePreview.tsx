import { useEffect, useLayoutEffect, useRef, useState, type SyntheticEvent } from 'react'
import { useSubjectOutputs } from '@/api/outputs'
import { cn } from '@/lib/utils'
import { imageLinks, type ImageLink } from '@/panel/images'
import { closeLayer, getUi, setUi, useUi } from '@/state/ui'

function Icon({ d }: { d: string }) {
  return (
    <svg className="ql-ic" viewBox="0 0 16 16" aria-hidden="true">
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

const I_X = 'M4.5 4.5l7 7M11.5 4.5l-7 7'
const I_LEFT = 'M10 3.5 5.5 8l4.5 4.5'
const I_RIGHT = 'M6 3.5 10.5 8 6 12.5'
const I_ACTUAL = 'M9.5 2.5h4v4M13.5 2.5 9 7M6.5 13.5h-4v-4M2.5 13.5 7 9'

/** The link of an image in « Liens » (its output id), to give it the focus back. */
function linkOf(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`a[data-img="${CSS.escape(id)}"]`)
}

/** Moves the preview to another image, within bounds. */
function go(index: number, count: number) {
  const p = getUi().preview
  if (!p || index < 0 || index >= count) return
  setUi({ preview: { ...p, index } })
}

/**
 * The image preview (Quick Look, t4 `viewer.js`): a modal window over every layer, the file name and « k / n » in its
 * head, ← → on its sides, the caption, natural size and zoom in its foot. z or a click on the image toggles the real
 * size (the image scrolls inside); Esc (handleKey) or Space close it and give the focus back to the last image's link.
 * The image is loaded by `<img src>` only: no pixel is read.
 */
export function ImagePreview({ images, index }: { images: ImageLink[]; index: number }) {
  const i = Math.max(0, Math.min(index, images.length - 1))
  const m = images[i]
  const [real, setReal] = useState(false)
  const [size, setSize] = useState<{ w: number; h: number } | null>(null)
  const [failed, setFailed] = useState(false)
  const [fit, setFit] = useState<number | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const win = useRef<HTMLDivElement>(null)
  const seen = useRef(m.id)
  seen.current = m.id

  // a new image: its own size and load state
  useLayoutEffect(() => {
    setSize(null)
    setFailed(false)
    setFit(null)
  }, [m.id])

  // focus into the window on open; back to the last image's link on close
  useEffect(() => {
    win.current?.focus()
    return () => linkOf(seen.current)?.focus()
  }, [])

  // modal for real: everything behind the window is inert while it is open (no Tab, click or Space reaches the sheet)
  useEffect(() => {
    const set: Element[] = []
    for (let n: Element | null = win.current; n && n !== document.body; n = n.parentElement) {
      for (const sib of Array.from(n.parentElement?.children ?? [])) {
        if (sib === n || sib.classList.contains('ql-veil') || sib.hasAttribute('inert')) continue
        sib.setAttribute('inert', '')
        set.push(sib)
      }
    }
    return () => set.forEach((e) => e.removeAttribute('inert'))
  }, [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Tab cycles inside the window
      if (e.key === 'Tab' && !e.metaKey && !e.ctrlKey && !e.altKey) {
        const w = win.current
        if (!w) return
        const f = Array.from(w.querySelectorAll<HTMLElement>('button'))
        if (!f.length) return
        const at = document.activeElement
        const first = f[0]
        const last = f[f.length - 1]
        if (!w.contains(at) || at === w) {
          e.preventDefault()
          ;(e.shiftKey ? last : first).focus()
        } else if (e.shiftKey && at === first) {
          e.preventDefault()
          last.focus()
        } else if (!e.shiftKey && at === last) {
          e.preventDefault()
          first.focus()
        }
        return
      }
      // the key that opened the preview (Space on a link) is already used
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return
      if (e.key === ' ') {
        // a focused button keeps its own Space
        if (e.target instanceof Element && e.target.closest('button')) return
        e.preventDefault()
        closeLayer()
      } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        e.preventDefault()
        const p = getUi().preview
        if (p) go(p.index + (e.key === 'ArrowRight' ? 1 : -1), images.length)
      } else if (e.key === 'z' || e.key === 'Z') {
        e.preventDefault()
        setReal((r) => !r)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [images.length])

  const onLoad = (e: SyntheticEvent<HTMLImageElement>) => {
    const img = e.currentTarget
    const w = img.naturalWidth
    const h = img.naturalHeight
    if (!w || !h) return
    setSize({ w, h })
    const b = box.current?.getBoundingClientRect()
    if (b && b.width > 0 && b.height > 0) setFit(Math.min(100, Math.round(Math.min(b.width / w, b.height / h) * 100)))
  }

  const nav = (dir: -1 | 1) => {
    const off = dir < 0 ? i === 0 : i === images.length - 1
    return (
      <button
        type="button"
        className={cn('ql-nav', dir < 0 ? 'ql-left' : 'ql-right')}
        aria-label={dir < 0 ? 'Image précédente (←)' : 'Image suivante (→)'}
        aria-disabled={off ? 'true' : undefined}
        onClick={() => {
          if (!off) go(i + dir, images.length)
        }}
      >
        <Icon d={dir < 0 ? I_LEFT : I_RIGHT} />
      </button>
    )
  }

  return (
    <>
      <div className="ql-veil" aria-hidden="true" onClick={() => closeLayer()} />
      <div ref={win} className={cn('ql', real && 'real')} role="dialog" aria-modal="true" aria-label={`Aperçu de ${m.caption}`} tabIndex={-1}>
        <div className="ql-h">
          <button type="button" className="ib" aria-label="Fermer (Esc)" onClick={() => closeLayer()}>
            <Icon d={I_X} />
          </button>
          <span className="ql-n">
            <b>{m.caption}</b>
            <span className="ql-i">
              {i + 1} / {images.length}
            </span>
          </span>
          <button type="button" className="ql-z" aria-pressed={real} onClick={() => setReal((r) => !r)}>
            <Icon d={I_ACTUAL} />
            Taille réelle <kbd>z</kbd>
          </button>
        </div>
        <div className="ql-st">
          <div className="ql-b" ref={box}>
            {failed ? (
              <p className="ql-err">Image indisponible.</p>
            ) : (
              <img key={m.id} className="ql-img" src={m.url} alt={m.caption} onLoad={onLoad} onError={() => setFailed(true)} onClick={() => setReal((r) => !r)} />
            )}
          </div>
          {nav(-1)}
          {nav(1)}
        </div>
        <div className="ql-f">
          <span className="ql-c">{m.caption}</span>
          <span className="ql-meta">
            {size ? `${size.w} × ${size.h} · ` : ''}
            {real ? 'taille réelle' : fit !== null ? `ajustée à ${fit} %` : 'ajustée'}
          </span>
          <span className="ql-sp" />
          <span className="ql-hint">
            <kbd>←</kbd>
            <kbd>→</kbd> image
          </span>
          <span className="ql-hint">
            <kbd>Esc</kbd> fermer
          </span>
        </div>
      </div>
    </>
  )
}

/** The preview of the open subject's images, mounted by the shell while `ui.preview` is set. */
export function PreviewLayer() {
  const p = useUi((s) => s.preview)
  const q = useSubjectOutputs(p?.subjectId ?? '')
  const images = p ? imageLinks(q.data ?? []) : []
  const empty = !!p && !q.isPending && !images.length
  // nothing to show: drop the preview, or handleKey would swallow the keys behind an empty layer
  useEffect(() => {
    if (empty) setUi({ preview: null })
  }, [empty])
  if (!p || !images.length) return null
  return <ImagePreview images={images} index={p.index} />
}
