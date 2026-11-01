import { act, fireEvent, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { getUi, openPanel, setUi } from '@/state/ui'
import { makeBacklog } from '@/test/backlog'
import { renderApp } from '@/test/render'

function key(k: string, init: Partial<KeyboardEventInit> = {}) {
  fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...init })
}

const sheet = () => document.querySelector('.lpanel')
const palette = () => screen.queryByRole('dialog', { name: 'Palette de commandes' })

describe('Esc closes one layer at a time (D14)', () => {
  it('the palette over the sheet: Esc closes the palette only, a second Esc the sheet', async () => {
    await renderApp()
    act(() => openPanel('pig-r1'))
    key('k', { code: 'KeyK', metaKey: true })
    expect(palette()).not.toBeNull()
    key('Escape')
    expect(palette()).toBeNull()
    expect(sheet()).not.toBeNull()
    key('Escape')
    expect(sheet()).toBeNull()
  })

  it('the palette opened with the keyboard over the floating sidebar: one Esc closes the palette only, a second the sidebar', async () => {
    await renderApp(undefined, { width: 1024 })
    key('b', { code: 'KeyB', metaKey: true })
    expect(getUi().floatOpen).toBe(true)
    key('k', { code: 'KeyK', metaKey: true })
    expect(palette()).not.toBeNull()
    expect(getUi().floatOpen).toBe(true)
    key('Escape')
    expect(palette()).toBeNull()
    expect(getUi().floatOpen).toBe(true)
    key('Escape')
    expect(getUi().floatOpen).toBe(false)
  })

  it('Réglages opened from the floating sidebar: the drawer stays mounted, Esc closes Réglages then the drawer', async () => {
    await renderApp(undefined, { width: 1024 })
    key('b', { code: 'KeyB', metaKey: true })
    expect(document.querySelector('aside.side')).not.toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /Réglages/ }))
    expect(getUi().settings).toBe(true)
    expect(getUi().floatOpen).toBe(true)
    expect(document.querySelector('aside.side')).not.toBeNull()
    expect(screen.getByRole('dialog', { name: 'Réglages' })).not.toBeNull()
    key('Escape')
    expect(getUi().settings).toBe(false)
    expect(document.querySelector('aside.side')).not.toBeNull()
    key('Escape')
    expect(document.querySelector('aside.side')).toBeNull()
  })

  it('a menu over the floating sidebar: Esc closes the menu first', async () => {
    await renderApp(undefined, { width: 1024 })
    act(() => setUi({ floatOpen: true, menu: 'group' }))
    key('Escape')
    expect(getUi().menu).toBeNull()
    expect(getUi().floatOpen).toBe(true)
  })

  it('a menu over the sheet: Esc closes the menu only', async () => {
    await renderApp()
    act(() => openPanel('pig-r1'))
    ;(document.querySelector('li.lr6[data-id="pig-r1"]') as HTMLElement | null)?.focus()
    key('v')
    expect(getUi().menu).toBe('group')
    key('Escape')
    expect(getUi().menu).toBeNull()
    expect(sheet()).not.toBeNull()
  })

  it('Esc in the palette keeps the search and the filters (D14)', async () => {
    await renderApp()
    act(() => setUi({ query: 'atlas', statusF: 'ouvert' }))
    key('k', { code: 'KeyK', metaKey: true })
    key('Escape')
    expect(palette()).toBeNull()
    expect(getUi().query).toBe('atlas')
    expect(getUi().statusF).toBe('ouvert')
  })

  it('Réglages: palette > menu > Réglages > floating sidebar > sheet, one Esc each', async () => {
    await renderApp(undefined, { width: 1024 })
    act(() => openPanel('pig-r1'))
    act(() => setUi({ floatOpen: true, menu: 'group', settings: true, palette: true }))
    key('Escape')
    expect(getUi().palette).toBe(false)
    expect(getUi().settings).toBe(true)
    key('Escape')
    expect(getUi().menu).toBeNull()
    expect(getUi().settings).toBe(true)
    key('Escape')
    expect(getUi().settings).toBe(false)
    expect(getUi().floatOpen).toBe(true)
    key('Escape')
    expect(getUi().floatOpen).toBe(false)
    expect(sheet()).not.toBeNull()
    key('Escape')
    expect(sheet()).toBeNull()
  })

  it('the image preview first: preview > palette > Réglages > sheet, one Esc each', async () => {
    const data = makeBacklog()
    data.outputs = { 'pig-r1': [{ seq: 1, id: 'o1', subject_id: 'pig-r1', kind: 'capture', url: 'https://x.example/a.png', key: 'k1', title: 'A', ts: 0 }] }
    await renderApp(data)
    act(() => openPanel('pig-r1'))
    act(() => setUi({ settings: true, palette: true, preview: { subjectId: 'pig-r1', index: 0 } }))
    expect(await screen.findByRole('dialog', { name: /^Aperçu de / })).not.toBeNull()
    key('Escape')
    expect(getUi().preview).toBeNull()
    expect(getUi().palette).toBe(true)
    key('Escape')
    expect(getUi().palette).toBe(false)
    expect(getUi().settings).toBe(true)
    key('Escape')
    expect(getUi().settings).toBe(false)
    expect(sheet()).not.toBeNull()
    key('Escape')
    expect(sheet()).toBeNull()
  })
})

describe('Esc and the session panel', () => {
  it('the palette over the session panel: Esc closes the palette only, a second Esc the panel; the panel sits in the sheet slot (.pwrap, same z stack)', async () => {
    const { withSessions } = await import('@/test/peers-fixture')
    await renderApp(withSessions())
    key('s')
    await screen.findByRole('region', { name: 'Sessions' })
    fireEvent.click(document.querySelector<HTMLElement>('li.srow[data-sid="s-busy-q"]')!)
    expect(document.querySelector('.pwrap > .spanel')).not.toBeNull()
    key('k', { code: 'KeyK', metaKey: true })
    expect(palette()).not.toBeNull()
    key('Escape')
    expect(palette()).toBeNull()
    expect(document.querySelector('.spanel')).not.toBeNull()
    key('Escape')
    expect(document.querySelector('.spanel')).toBeNull()
    expect(getUi().view).toBe('sessions')
  })

  it('Réglages over the session panel: Esc closes Réglages first, the panel stays; a second Esc closes the panel', async () => {
    const { withSessions } = await import('@/test/peers-fixture')
    await renderApp(withSessions())
    key('s')
    await screen.findByRole('region', { name: 'Sessions' })
    fireEvent.click(document.querySelector<HTMLElement>('li.srow[data-sid="s-busy-q"]')!)
    expect(document.querySelector('.pwrap > .spanel')).not.toBeNull()
    key(',', { code: 'Comma', metaKey: true })
    expect(screen.getByRole('dialog', { name: 'Réglages' })).not.toBeNull()
    key('Escape')
    expect(screen.queryByRole('dialog', { name: 'Réglages' })).toBeNull()
    expect(getUi().settings).toBe(false)
    expect(document.querySelector('.spanel')).not.toBeNull()
    key('Escape')
    expect(document.querySelector('.spanel')).toBeNull()
    expect(getUi().view).toBe('sessions')
  })
})
