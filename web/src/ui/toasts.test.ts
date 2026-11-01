import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/client'
import { COPY } from '@/api/refusals'
import { getToasts, pushToast, resetToasts, UNDO_MS, undoLast } from './toasts'

function undoable(title: string, run = vi.fn(async () => {}), stillValid: () => boolean | Promise<boolean> = () => true) {
  return { text: COPY.rankMoved(title), undo: { title, label: COPY.rankRestored(title), run, stillValid } }
}

const texts = () => getToasts().map((t) => t.text)

describe('toasts', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    resetToasts()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('an undoable toast lasts 8 s; undoLast after 8 s does nothing', async () => {
    const run = vi.fn(async () => {})
    pushToast(undoable('A', run))
    expect(UNDO_MS).toBe(8000)
    vi.advanceTimersByTime(7999)
    expect(texts()).toEqual(['Rang de « A » modifié'])
    vi.advanceTimersByTime(1)
    expect(texts()).toEqual([])
    await undoLast()
    expect(run).not.toHaveBeenCalled()
  })

  it('a rank changed toast (D23) is undoable; undoLast runs it and says « Annulé · Rang de « X » rétabli »', async () => {
    const run = vi.fn(async () => {})
    pushToast(undoable('A', run))
    expect(getToasts()[0].undo).toBeDefined()
    await undoLast()
    expect(run).toHaveBeenCalledTimes(1)
    expect(texts()).toEqual(['Annulé · Rang de « A » rétabli'])
  })

  it('undoLast acts on the last toast only, and only once', async () => {
    const a = vi.fn(async () => {})
    const b = vi.fn(async () => {})
    pushToast(undoable('A', a))
    pushToast(undoable('B', b))
    await undoLast()
    await undoLast()
    expect(b).toHaveBeenCalledTimes(1)
    expect(a).not.toHaveBeenCalled()
  })

  it('a newer plain toast hides the undoable one: undoLast then does nothing', async () => {
    const a = vi.fn(async () => {})
    pushToast(undoable('A', a))
    pushToast({ text: 'Maintenance en cours : réessaie quand elle est finie.' })
    await undoLast()
    expect(a).not.toHaveBeenCalled()
  })

  it('stillValid false: no run, toast D9-9', async () => {
    const run = vi.fn(async () => {})
    pushToast(undoable('A', run, () => false))
    await undoLast()
    expect(run).not.toHaveBeenCalled()
    expect(texts()).toEqual(["Impossible d'annuler : « A » a changé depuis."])
  })

  it('stillValid async resolving false: no run, toast D9-9', async () => {
    const run = vi.fn(async () => {})
    pushToast(undoable('A', run, async () => false))
    await undoLast()
    expect(run).not.toHaveBeenCalled()
    expect(texts()).toEqual(["Impossible d'annuler : « A » a changé depuis."])
  })

  it('a refused undo shows its refusal (RANK_CHANGED on an undo = D9-9)', async () => {
    const run = vi.fn(async () => {
      throw new ApiError(409, 'stale', 'RANK_CHANGED')
    })
    pushToast(undoable('A', run))
    await undoLast()
    expect(texts()).toEqual(["Impossible d'annuler : « A » a changé depuis."])
  })

  it('a plain toast goes after a few seconds', () => {
    pushToast({ text: 'x' })
    expect(texts()).toEqual(['x'])
    vi.advanceTimersByTime(UNDO_MS)
    expect(texts()).toEqual([])
  })
})
