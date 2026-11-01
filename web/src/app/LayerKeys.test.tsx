import { act, fireEvent, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import * as toasts from '@/ui/toasts'
import { getUi, openPanel, setUi } from '@/state/ui'
import { makeBacklog } from '@/test/backlog'
import { renderApp } from '@/test/render'

const key = (k: string, init: Partial<KeyboardEventInit> = {}) => fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...init })
const dlg = () => screen.queryByRole('dialog', { name: 'Réglages' })

/** Réglages open: every list key is inert (handleKey returns early), only Esc and ⌘, answer. */
describe('Réglages open: the list keys do nothing', () => {
  it.each(['z', 'l', 'f', 'v', 'g', '?', ',', 'j', 'k', 'c', 'h', 'ArrowLeft', 'ArrowRight', 'Enter'])('%s', async (k) => {
    const undo = vi.spyOn(toasts, 'undoLast')
    await renderApp()
    act(() => {
      openPanel('pig-r1')
    })
    key(',', { metaKey: true })
    expect(dlg()).not.toBeNull()
    document.body.focus()
    const before = { ...getUi() }
    key(k)
    const after = getUi()
    expect(undo).not.toHaveBeenCalled()
    expect(after.menu).toBe(before.menu)
    expect(after.kbHidden).toBe(before.kbHidden)
    expect(after.sel).toBe(before.sel)
    expect(after.open).toEqual(before.open)
    expect(after.palette).toBe(before.palette)
    expect(after.panel).toBe(before.panel)
    expect(dlg()).not.toBeNull()
    key('Escape')
    expect(dlg()).toBeNull()
  })
})

/** ⌘K stays live above Réglages: the palette (z 10) opens over the popover (z 9); Esc peels palette, then Réglages. */
describe('Réglages open: ⌘K opens the palette over it', () => {
  it('⌘K opens the palette, Réglages stay; Esc closes the palette alone, then Réglages', async () => {
    await renderApp()
    key(',', { metaKey: true })
    expect(dlg()).not.toBeNull()
    key('k', { metaKey: true })
    expect(getUi().palette).toBe(true)
    expect(dlg()).not.toBeNull()
    key('Escape')
    expect(getUi().palette).toBe(false)
    expect(dlg()).not.toBeNull()
    key('Escape')
    expect(dlg()).toBeNull()
  })

  it('⌘B stays ignored while Réglages are open', async () => {
    await renderApp()
    key(',', { metaKey: true })
    const before = { sideClosed: getUi().sideClosed, sideDocked: getUi().sideDocked, floatOpen: getUi().floatOpen }
    expect(Object.values(before).some((v) => v !== undefined)).toBe(true) // positive control
    key('b', { metaKey: true })
    expect({ sideClosed: getUi().sideClosed, sideDocked: getUi().sideDocked, floatOpen: getUi().floatOpen }).toEqual(before)
  })
})

/** Image preview open: handleKey returns early but for Esc, Space, ←, →, z, which keep only their preview meaning. */
describe('Image preview open: the list keys do nothing', () => {
  const imgs = () => {
    const data = makeBacklog()
    data.outputs = {
      'pig-r1': [1, 2, 3].map((i) => ({ seq: i, id: `o${i}`, subject_id: 'pig-r1', kind: 'capture', url: `https://x.example/${i}.png`, key: `k${i}`, title: `I${i}`, ts: 0 })),
    }
    return data
  }
  const prev = () => screen.queryByRole('dialog', { name: /^Aperçu de / })

  async function openPreview(index = 1) {
    const r = await renderApp(imgs())
    act(() => openPanel('pig-r1'))
    act(() => setUi({ preview: { subjectId: 'pig-r1', index } }))
    expect(await screen.findByRole('dialog', { name: /^Aperçu de / })).not.toBeNull()
    document.body.focus()
    return r
  }

  // '?' binds nothing today: kept only to guard a future binding
  it.each(['l', 'Enter', 'f', 'v', 'g', '?', ',', 'j', 'k', 'c', 'h'])('%s', async (k) => {
    const undo = vi.spyOn(toasts, 'undoLast')
    const r = await openPreview()
    const before = { ...getUi() }
    const writes = r.writes().length
    key(k)
    const after = getUi()
    expect(undo).not.toHaveBeenCalled()
    expect(r.writes()).toHaveLength(writes)
    for (const f of ['menu', 'kbHidden', 'sel', 'palette', 'panel', 'settings', 'preview'] as const) expect(after[f]).toEqual(before[f])
    expect(after.open).toEqual(before.open)
    expect(prev()).not.toBeNull()
  })

  it('Enter does not open the sheet while the preview is open (the sheet is closed first, so a leak would show)', async () => {
    await openPreview()
    act(() => setUi({ panel: false }))
    const before = { panel: getUi().panel, sel: getUi().sel }
    expect(before.panel).toBe(false) // positive control: nothing is open behind the preview
    key('Enter')
    expect(getUi().panel).toBe(before.panel)
    expect(getUi().sel).toEqual(before.sel)
    expect(prev()).not.toBeNull()
  })

  it('z only toggles the size: undoLast is never called', async () => {
    const undo = vi.spyOn(toasts, 'undoLast')
    await openPreview()
    key('z')
    expect(undo).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /Taille réelle/ })).toHaveAttribute('aria-pressed', 'true')
  })

  it('← / → change the image and fold no group', async () => {
    await openPreview(1)
    const open = { ...getUi().open }
    key('ArrowRight')
    expect(getUi().preview?.index).toBe(2)
    key('ArrowLeft')
    key('ArrowLeft')
    expect(getUi().preview?.index).toBe(0)
    expect(getUi().open).toEqual(open)
  })

  it('⌘K and ⌘, wait for the preview to close', async () => {
    await openPreview()
    key('k', { code: 'KeyK', metaKey: true })
    key(',', { metaKey: true })
    expect(getUi().palette).toBe(false)
    expect(getUi().settings).toBe(false)
    key('Escape')
    expect(prev()).toBeNull()
  })
})
