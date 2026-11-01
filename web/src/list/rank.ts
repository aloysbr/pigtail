import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useSyncExternalStore, type DragEvent } from 'react'
import { ApiError } from '@/api/client'
import { useSend, useWrite } from '@/api/mutate'
import { refusalToast, COPY } from '@/api/refusals'
import type { Peer, Space, Subject, Theme } from '@/api/types'
import type { Group, Row } from '@/domain/group'
import { applyChanges, planDrop, planMove, type RankChange, type RankLine } from '@/domain/rank'
import { getUi, setUi } from '@/state/ui'
import { pushToast } from '@/ui/toasts'
import { buildModel, type ListModel } from './model'

let memo: { inputs: unknown[]; model: ListModel | null } | null = null

/** The list model of what the cache holds now, with the current UI state (memoised: dragover asks it often). */
function modelOf(qc: QueryClient): ListModel | null {
  const subjects = qc.getQueryData<Subject[]>(['subjects'])
  const themes = qc.getQueryData<Theme[]>(['themes']) ?? []
  const spaces = qc.getQueryData<Space[]>(['spaces']) ?? []
  const peers = qc.getQueryData<Peer[]>(['peers']) ?? []
  const ui = getUi()
  const inputs = [subjects, themes, spaces, peers, ui]
  if (memo && memo.inputs.every((x, i) => x === inputs[i])) return memo.model
  const model = subjects ? buildModel({ subjects, themes, spaces, peers }, ui) : null
  memo = { inputs, model }
  return model
}

function leafOf(groups: readonly Group[], id: string): Group | undefined {
  for (const g of groups) {
    if (g.children.length) {
      const l = leafOf(g.children, id)
      if (l) return l
    } else if (g.rows.some((r) => r.s.id === id)) return g
  }
  return undefined
}

/** What a rank plan needs for a line: its group's En cours + Ouvert lines in display order, the subjects of its scope, the row. */
function rankContext(qc: QueryClient, id: string): { lines: RankLine[]; scope: Subject[]; row: Row } | null {
  const model = modelOf(qc)
  if (!model) return null
  const g = leafOf(model.groups, id)
  const row = g?.rows.find((r) => r.s.id === id)
  if (!g || !row) return null
  // the scope a row shows (its subject's, else its theme's) is the one the plan compares
  const withScope = (r: Row): Subject => ({ ...r.s, scope: r.scope })
  const lines = g.rows.filter((r) => r.d === 'cours' || r.d === 'ouvert').map((r) => ({ subject: withScope(r), display: r.d }))
  const scope = model.rows.filter((r) => r.scope === row.scope).map(withScope)
  return { lines, scope, row }
}

/** The changes a drop of dragId on overId would write; [] when the target is refused (D20, D22). */
export function dropPlan(qc: QueryClient, dragId: string, overId: string): RankChange[] {
  const ctx = rankContext(qc, dragId)
  return ctx ? planDrop(ctx.lines, ctx.scope, dragId, overId) : []
}

const invert = (changes: readonly RankChange[]): RankChange[] => changes.map((c) => ({ id: c.id, from: c.to, to: c.from }))

/** Writes rank changes (POST /subjects/ranks), optimistic, then toasts « Rang de « X » modifié » with its undo (D23), or the refusal. */
function useRanks(): (changes: RankChange[], row: Row) => Promise<void> {
  const qc = useQueryClient()
  const send = useSend()
  const w = useWrite<RankChange[], Subject[]>({
    mutationFn: (changes) => send<Subject[]>('POST', '/subjects/ranks', { changes }),
    optimistic: (changes) => {
      const prev = qc.getQueryData<Subject[]>(['subjects'])
      if (prev) qc.setQueryData(['subjects'], applyChanges(prev, changes))
      return () => qc.setQueryData(['subjects'], prev)
    },
    invalidate: [['subjects']],
  })
  return async (changes, row) => {
    const title = row.short
    const write = async (c: RankChange[]) => {
      await w.mutateAsync(c)
    }
    // the selection follows its line
    setUi((s) => ({ sel: row.s.id, focusTick: s.focusTick + 1 }))
    try {
      await write(changes)
    } catch (e) {
      const err = e instanceof ApiError ? e : new ApiError(0, String(e))
      pushToast({ text: refusalToast(err, { title, scope: row.scope, action: 'move' }) })
      return
    }
    pushToast({
      text: COPY.rankMoved(title),
      undo: {
        title,
        label: COPY.rankRestored(title),
        run: () => write(invert(changes)),
        stillValid: () => {
          const cur = qc.getQueryData<Subject[]>(['subjects']) ?? []
          return changes.every((c) => (cur.find((s) => s.id === c.id)?.rank ?? 0) === c.to)
        },
      },
    })
  }
}

/** ⌥↑ (-1) / ⌥↓ (1) on a line: the planMove changes, or nothing when the plan is empty (edge, border, scope, unranked). */
export function useMoveRank(): (id: string, dir: -1 | 1) => void {
  const qc = useQueryClient()
  const ranks = useRanks()
  return (id, dir) => {
    const ctx = rankContext(qc, id)
    if (!ctx) return
    const changes = planMove(ctx.lines, ctx.scope, id, dir)
    if (changes.length) void ranks(changes, ctx.row)
  }
}

/** A drop of dragId on overId: the planDrop changes, or nothing when the plan is empty. */
export function useDropRank(): (dragId: string, overId: string) => void {
  const qc = useQueryClient()
  const ranks = useRanks()
  return (dragId, overId) => {
    const ctx = rankContext(qc, dragId)
    if (!ctx) return
    const changes = planDrop(ctx.lines, ctx.scope, dragId, overId)
    if (changes.length) void ranks(changes, ctx.row)
  }
}

/** The drag in progress: the dragged row and the allowed target under the pointer. */
interface DragState {
  drag: string | null
  over: string | null
}

let drag: DragState = { drag: null, over: null }
const listeners = new Set<() => void>()

function setDrag(p: Partial<DragState>) {
  drag = { ...drag, ...p }
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** Reads a slice of the drag state. */
export function useDrag<T>(sel: (s: DragState) => T): T {
  return useSyncExternalStore(subscribe, () => sel(drag), () => sel(drag))
}

/** What a drop calls: useDropRank's function, made once by the Shell. */
export type OnDrop = (dragId: string, overId: string) => void

/** The drag handlers of a row (mouse only; spec § Réordonner, D12, D22): dragged row dimmed, allowed target marked, refused target left to the browser. Off when onDrop is null. */
export function useRowDrag(id: string, onDrop: OnDrop | null) {
  const qc = useQueryClient()
  const dragging = useDrag((s) => s.drag === id)
  const over = useDrag((s) => s.over === id)
  if (!onDrop) return { dragging: false, over: false, props: {} }
  return {
    dragging,
    over,
    props: {
      draggable: true,
      onDragStart: (e: DragEvent) => {
        setDrag({ drag: id, over: null })
        if (e.dataTransfer) {
          e.dataTransfer.effectAllowed = 'move'
          e.dataTransfer.setData('text/plain', id)
        }
      },
      onDragOver: (e: DragEvent) => {
        const d = drag.drag
        if (d && d !== id && dropPlan(qc, d, id).length) {
          // allowed: the browser accepts the drop only once dragover is cancelled
          e.preventDefault()
          if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
          if (drag.over !== id) setDrag({ over: id })
          return
        }
        // refused (D22): no drop mark, the browser's own refusal
        if (e.dataTransfer) e.dataTransfer.dropEffect = 'none'
        if (drag.over === id) setDrag({ over: null })
      },
      onDragLeave: () => {
        if (drag.over === id) setDrag({ over: null })
      },
      onDrop: (e: DragEvent) => {
        const d = drag.drag
        e.preventDefault()
        setDrag({ drag: null, over: null })
        if (d && d !== id) onDrop(d, id)
      },
      onDragEnd: () => setDrag({ drag: null, over: null }),
    },
  }
}
