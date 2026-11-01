import { useSyncExternalStore } from 'react'
import { ApiError } from '@/api/client'
import { COPY, refusalToast } from '@/api/refusals'

/** How long a toast stays, undoable or not (D13: the undo window is 8 s). */
export const UNDO_MS = 8000

/** The undo of an action: the subject's short title, the restored object (« Rang de « X » rétabli »), the write, and whether it still applies. */
export interface Undo {
  title: string
  label: string
  run: () => Promise<void>
  /** Checked right before run; false = the value changed since (D9-9), nothing is written. */
  stillValid: () => boolean | Promise<boolean>
}

/** One toast on screen. */
export interface ToastItem {
  id: string
  text: string
  undo?: Undo
  expires: number
}

let current: ToastItem | null = null
let timer: ReturnType<typeof setTimeout> | null = null
let seq = 0
const listeners = new Set<() => void>()
const EMPTY: ToastItem[] = []
let snapshot: ToastItem[] = EMPTY

function emit() {
  snapshot = current ? [current] : EMPTY
  for (const l of listeners) l()
}

function clear(id: string) {
  if (current?.id !== id) return
  current = null
  timer = null
  emit()
}

/** Shows a toast (one at a time, as t10: a new one replaces the last); returns its id. */
export function pushToast(t: { text: string; undo?: Undo }): string {
  seq += 1
  const id = `t${seq}`
  if (timer) clearTimeout(timer)
  current = { id, text: t.text, undo: t.undo, expires: Date.now() + UNDO_MS }
  timer = setTimeout(() => clear(id), UNDO_MS)
  emit()
  return id
}

/**
 * z or « annuler »: undoes the action of the toast on screen if it is undoable and still within its 8 s.
 * The undo is spent at once (a second z does nothing); stillValid false = toast D9-9, no write;
 * a refused write = its refusal toast; success = « Annulé · <objet> » (D9-10).
 */
export async function undoLast(): Promise<void> {
  const t = current
  if (!t?.undo || Date.now() >= t.expires) return
  const undo = t.undo
  current = { ...t, undo: undefined }
  emit()
  let valid: boolean
  try {
    valid = await undo.stillValid()
  } catch {
    valid = false
  }
  if (!valid) {
    pushToast({ text: COPY.undoStale(undo.title) })
    return
  }
  try {
    await undo.run()
  } catch (e) {
    const err = e instanceof ApiError ? e : new ApiError(0, String(e))
    pushToast({ text: refusalToast(err, { title: undo.title, action: 'undo' }) })
    return
  }
  pushToast({ text: COPY.undone(undo.label) })
}

/** True while the toast on screen can be undone (the footer's « z annuler »). */
export function hasUndo(): boolean {
  return !!current?.undo && Date.now() < current.expires
}

/** The toasts on screen (tests, the host). */
export function getToasts(): ToastItem[] {
  return snapshot
}

/** Clears every toast (tests). */
export function resetToasts() {
  if (timer) clearTimeout(timer)
  timer = null
  current = null
  emit()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** The toasts on screen, re-rendered on every change. */
export function useToasts(): ToastItem[] {
  return useSyncExternalStore(subscribe, getToasts, getToasts)
}
