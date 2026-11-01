import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Output, OutputGroup, OutputTour } from '@/api/types'
import { getUi } from '@/state/ui'
import { makeBacklog } from '@/test/backlog'
import { groupButton, renderApp } from '@/test/render'
import { blocks } from '@/theme/css-parse'
import { pushToast, resetToasts } from '@/ui/toasts'
import { READER_KEEP } from './Reader'

const ROOT = '/Users/x/_outputs/app-macos-atlas/mockup'
const SERVED = 'http://127.0.0.1:7801'

/** The rounds t3…t23 of one mockup (or the given numbers), the latest yesterday 15:11. */
function rounds(ns: number[] = Array.from({ length: 21 }, (_, i) => i + 3), missing: number[] = []): { outputs: Output[]; group: OutputGroup } {
  const yesterday = new Date()
  yesterday.setDate(yesterday.getDate() - 1)
  yesterday.setHours(15, 11, 0, 0)
  const top = ns[ns.length - 1]
  const tours: OutputTour[] = []
  const outputs: Output[] = []
  for (const n of ns) {
    const ts = yesterday.getTime() - (top - n) * 3_600_000
    const path = `${ROOT}/t${n}/index.html`
    const exists = !missing.includes(n)
    const served_url = exists ? `${SERVED}${ROOT}/t${n}/` : undefined
    outputs.push({ seq: n, id: `out_t${n}`, subject_id: 'pig-r1', kind: 'mockup', key: path, path, title: `mockup t${n}`, ts, exists, served_url })
    tours.push({ n, label: `t${n}`, output_id: `out_t${n}`, title: `mockup t${n}`, ts, path, exists, served_url })
  }
  return { outputs, group: { key: ROOT, name: 'app-macos-atlas', kind: 'mockup', first_seq: ns[0], tours } }
}

async function openReader(r = rounds(), beforeOpen?: () => void) {
  const data = makeBacklog()
  data.outputs = { 'pig-r1': r.outputs }
  data.groups = { 'pig-r1': [r.group] }
  const c = await renderApp(data)
  await c.user.click(groupButton('th:th_pig'))
  await c.user.click(document.querySelector('li[data-id="pig-r1"]') as HTMLElement)
  const row = await screen.findByRole('button', { name: 'app-macos-atlas' })
  beforeOpen?.()
  await c.user.click(row)
  const dlg = await screen.findByRole('dialog', { name: /Maquette app-macos-atlas/ })
  return { ...c, dlg, row }
}

function frame(dlg: HTMLElement): HTMLIFrameElement | null {
  return dlg.querySelector('iframe')
}

afterEach(() => resetToasts())

describe('Reader (D4): the full-page layer', () => {
  it('opens on the highest round: sandboxed iframe at its served_url, « dernière version »', async () => {
    const { dlg } = await openReader()
    expect(dlg).toHaveAttribute('aria-modal', 'true')
    const f = frame(dlg)
    expect(f).toHaveAttribute('src', `${SERVED}${ROOT}/t23/`)
    expect(f).toHaveAttribute('sandbox', 'allow-scripts')
    expect(f).toHaveAttribute('title', 'Maquette app-macos-atlas, tour t23')
    expect(dlg).toHaveTextContent('Maquette · 21 tours')
    expect(dlg).toHaveTextContent('mockup t23')
    expect(dlg).toHaveTextContent('dernière version')
    const ext = within(dlg).getByRole('link', { name: 'Ouvrir dans un onglet' })
    expect(ext).toHaveAttribute('href', `${SERVED}${ROOT}/t23/`)
    expect(ext).toHaveAttribute('target', '_blank')
    expect(ext).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('t19 from the menu, then the band, then back to the latest', async () => {
    const { dlg, user } = await openReader()
    await user.click(within(dlg).getByRole('button', { name: /^t23/ }))
    const menu = within(dlg).getByRole('menu', { name: 'Tours' })
    const items = within(menu).getAllByRole('menuitemradio')
    expect(items.length).toBe(21)
    expect(items[0]).toHaveTextContent('t23')
    expect(items[0]).toHaveTextContent('dernière')
    expect(items[0]).toHaveAttribute('aria-checked', 'true')
    expect(items[20]).toHaveTextContent('t3')
    await user.click(within(menu).getByRole('menuitemradio', { name: /^t19/ }))
    expect(within(dlg).queryByRole('menu')).toBeNull()
    expect(frame(dlg)).toHaveAttribute('src', `${SERVED}${ROOT}/t19/`)
    expect(dlg).toHaveTextContent('Version ancienne · t19, 4 tours avant la dernière')
    expect(dlg).not.toHaveTextContent('dernière version')
    await user.click(within(dlg).getByRole('button', { name: 'Revenir à la dernière' }))
    expect(frame(dlg)).toHaveAttribute('src', `${SERVED}${ROOT}/t23/`)
    expect(dlg).not.toHaveTextContent('Version ancienne')
  })

  it('Esc in the round menu closes the menu only; the next Esc closes the reader', async () => {
    const { dlg, user } = await openReader()
    await user.click(within(dlg).getByRole('button', { name: /^t23/ }))
    expect(within(dlg).getByRole('menuitemradio', { name: /^t23/ })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(within(dlg).queryByRole('menu')).toBeNull()
    expect(getUi().reader).not.toBeNull()
    expect(within(dlg).getByRole('button', { name: /^t23/ })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(getUi().reader).toBeNull()
  })

  it('← four times reaches t19; → goes back; ‹ is off on the first round', async () => {
    const { dlg } = await openReader()
    for (let i = 0; i < 4; i += 1) fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
    expect(frame(dlg)).toHaveAttribute('src', `${SERVED}${ROOT}/t19/`)
    expect(getUi().reader?.tour).toBe(19)
    fireEvent.keyDown(document.body, { key: 'ArrowRight' })
    expect(frame(dlg)).toHaveAttribute('src', `${SERVED}${ROOT}/t20/`)
    for (let i = 0; i < 30; i += 1) fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
    expect(frame(dlg)).toHaveAttribute('src', `${SERVED}${ROOT}/t3/`)
    expect(within(dlg).getByRole('button', { name: 'Tour précédent (←)' })).toHaveAttribute('aria-disabled', 'true')
  })

  it('one round: neither ‹ › nor menu, « 1 tour »', async () => {
    const { dlg } = await openReader(rounds([4]))
    expect(dlg).toHaveTextContent('Maquette · 1 tour')
    expect(within(dlg).queryByRole('button', { name: 'Tour précédent (←)' })).toBeNull()
    expect(within(dlg).queryByRole('button', { name: 'Tour suivant (→)' })).toBeNull()
    expect(within(dlg).queryByRole('button', { name: /^t4/ })).toBeNull()
  })

  it('a round gone from the disk: its scene, « introuvable » in the menu, the tab link off', async () => {
    const { dlg, user } = await openReader(rounds(undefined, [7]))
    await user.click(within(dlg).getByRole('button', { name: /^t23/ }))
    const item = within(within(dlg).getByRole('menu')).getByRole('menuitemradio', { name: /^t7/ })
    expect(item).toHaveTextContent('introuvable')
    await user.click(item)
    expect(frame(dlg)).toBeNull()
    expect(dlg).toHaveTextContent('t7 n’est plus sur le disque')
    expect(dlg).toHaveTextContent(`${ROOT}/t7/index.html`)
    const ext = within(dlg).getByText('Ouvrir dans un onglet').closest('a') as HTMLAnchorElement
    expect(ext).toHaveAttribute('aria-disabled', 'true')
    expect(ext).not.toHaveAttribute('href')
    await user.click(within(dlg).getByRole('button', { name: 'Voir la dernière version (t23)' }))
    expect(frame(dlg)).toHaveAttribute('src', `${SERVED}${ROOT}/t23/`)
  })

  it('Esc closes it and gives the focus back to its line', async () => {
    const { user } = await openReader()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: /Maquette app-macos-atlas/ })).toBeNull()
    expect(getUi().reader).toBeNull()
    await waitFor(() => expect(document.activeElement).toHaveAttribute('data-group', ROOT))
    // the sheet stays open under it
    expect(getUi().panel).toBe(true)
  })

  it('the line re-rendered while the reader is open still takes the focus back after Esc and ×, as a browser does (an inert element refuses focus)', async () => {
    // jsdom ignores inert; a browser does not focus an element under [inert]: emulate it
    const focus = HTMLElement.prototype.focus
    const spy = vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement, o?: FocusOptions) {
      if (this.closest('[inert]')) return
      focus.call(this, o)
    })
    try {
      const { user, refetch, data } = await openReader()
      // a re-read while the reader is open re-renders the line (one more round: « 22 tours »)
      const next = data()
      const more = rounds(Array.from({ length: 22 }, (_, i) => i + 3))
      await act(() => refetch({ ...next, outputs: { 'pig-r1': more.outputs }, groups: { 'pig-r1': [more.group] } }))
      await waitFor(() => expect(document.querySelector(`[data-group="${ROOT}"]`)?.closest('li')).toHaveTextContent('22 tours'))
      await user.keyboard('{Escape}')
      expect(getUi().reader).toBeNull()
      await waitFor(() => expect(document.activeElement).toBe(document.querySelector(`[data-group="${ROOT}"]`)))
      await user.click(document.querySelector(`[data-group="${ROOT}"]`) as HTMLElement)
      const dlg = await screen.findByRole('dialog', { name: /Maquette app-macos-atlas/ })
      await user.click(within(dlg).getByRole('button', { name: 'Fermer (Esc)' }))
      await waitFor(() => expect(document.activeElement).toBe(document.querySelector(`[data-group="${ROOT}"]`)))
    } finally {
      spy.mockRestore()
    }
  })

  it('the next opening from the sheet starts from the latest again', async () => {
    const { dlg, user, row } = await openReader()
    fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
    expect(frame(dlg)).toHaveAttribute('src', `${SERVED}${ROOT}/t22/`)
    await user.keyboard('{Escape}')
    await user.click(row)
    const again = await screen.findByRole('dialog', { name: /Maquette app-macos-atlas/ })
    expect(frame(again)).toHaveAttribute('src', `${SERVED}${ROOT}/t23/`)
  })

  it('everything behind is inert except the toasts and the session dock', async () => {
    // a stand-in for the session dock (lot 4), already on the page when the reader opens
    const dock = document.createElement('div')
    dock.setAttribute('data-session-dock', '')
    const { dlg } = await openReader(undefined, () => document.querySelector('.app')?.append(dock))
    act(() => {
      pushToast({ text: 'Réponse envoyée · la session reprend' })
    })
    const toast = await screen.findByRole('status')
    expect(toast.closest('[inert]')).toBeNull()
    expect(dlg.closest('[inert]')).toBeNull()
    const sheet = document.getElementById('sheet') as HTMLElement
    expect(sheet.closest('[inert]')).not.toBeNull()
    expect(document.querySelector('main.lmain')?.closest('[inert]')).not.toBeNull()
    expect(dock.matches(READER_KEEP)).toBe(true)
    expect(dock.closest('[inert]')).toBeNull()
    dock.remove()
  })

  it('a toast button stays clickable while the reader is open', async () => {
    const { user } = await openReader()
    const run = vi.fn(async () => {})
    act(() => {
      pushToast({ text: 'Rang rétabli', undo: { title: 'x', label: 'rang', run, stillValid: () => true } })
    })
    const btn = await screen.findByRole('button', { name: /annuler/ })
    expect(btn.closest('[inert]')).toBeNull()
    await user.click(btn)
    await waitFor(() => expect(run).toHaveBeenCalledTimes(1))
    // the reader is still there
    expect(screen.getByRole('dialog', { name: /Maquette app-macos-atlas/ })).toBeInTheDocument()
  })

  it('Tab stays inside the dialog', async () => {
    const { dlg, user } = await openReader()
    for (let i = 0; i < 12; i += 1) {
      await user.tab()
      expect(dlg.contains(document.activeElement)).toBe(true)
    }
  })
})

describe('reader.css: the toasts pass over the layer', () => {
  it('the toast host is mounted inside the layer while it is open, once', async () => {
    const { dlg } = await openReader()
    act(() => {
      pushToast({ text: 'Rang rétabli' })
    })
    await screen.findByRole('status')
    expect(document.querySelectorAll('.toast6').length).toBe(1)
    expect(dlg.querySelector('.toast6')).not.toBeNull()
  })

  const css = (f: string) => readFileSync(join(process.cwd(), 'src', f), 'utf8')
  const zOf = (file: string, sel: RegExp) => {
    const b = blocks(css(file)).find((x) => sel.test(x.selector.replace(/^@layer \w+ /, '').trim()))
    const m = b && /z-index:\s*(\d+)/.exec(b.body)
    return m ? Number(m[1]) : NaN
  }
  it("z-index: the toast container over the layer's bar (inside the layer), the layer at the palette's level, under the preview", () => {
    const rd = zOf('reader/reader.css', /^\.rd$/)
    const bar = zOf('reader/reader.css', /^\.rd-h$/)
    const toast = zOf('ui/toast.css', /^\.toast6$/)
    const palette = zOf('app/layout.css', /^\.palette$/)
    const preview = zOf('ui/preview.css', /^\.ql-veil$/)
    expect(toast).toBeGreaterThan(bar)
    expect(rd).toBeGreaterThanOrEqual(palette)
    expect(rd).toBeLessThan(preview)
  })
  it('globals.css imports reader.css', () => {
    expect(css('theme/globals.css')).toContain('@import "../reader/reader.css";')
  })
})

describe('the reader bar under 480 px (verify lot 3)', () => {
  const css = () => readFileSync(join(process.cwd(), 'src/reader/reader.css'), 'utf8')
  const NARROW = '@media (max-width: 479.98px)'
  const body = (sel: string, narrow: boolean) =>
    blocks(css())
      .filter((b) => {
        const s = b.selector.replace(/^@layer \w+ /, '').trim()
        return narrow ? s === `${NARROW} ${sel}` : s === sel
      })
      .map((b) => b.body)
      .join(';')

  it('the name, the count and the round are separate pieces; « Ouvrir dans un onglet » keeps its name as aria-label, its text in a span', async () => {
    const { dlg } = await openReader(rounds(undefined, [22]))
    expect(dlg.querySelector('.rd-nm')).toHaveTextContent('app-macos-atlas')
    expect(dlg.querySelector('.rd-sub')).toHaveTextContent('Maquette · 21 tours')
    expect(dlg.querySelector('.rd-sub')).toHaveTextContent('mockup t23')
    const ext = within(dlg).getByRole('link', { name: 'Ouvrir dans un onglet' })
    expect(ext).toHaveAttribute('aria-label', 'Ouvrir dans un onglet')
    expect(ext.querySelector('.rd-ext-t')).toHaveTextContent('Ouvrir dans un onglet')
    // the missing round: the same name, still off, no href
    fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
    const off = dlg.querySelector('.rd-tools a[aria-disabled="true"]') as HTMLElement
    expect(off).toHaveAttribute('aria-label', 'Ouvrir dans un onglet')
    expect(off).not.toHaveAttribute('href')
  })

  it('the name ellipses at every width', () => {
    const b = body('.rd-nm', false)
    expect(b).toMatch(/text-overflow:\s*ellipsis/)
    expect(b).toMatch(/overflow:\s*hidden/)
    expect(b).toMatch(/white-space:\s*nowrap/)
    expect(b).toMatch(/min-width:\s*0/)
  })

  it('under 480 px: the tab link is an icon button, the hints go, the count and the round stay on a second line', () => {
    expect(body('.rd-ext-t', true)).toMatch(/display:\s*none/)
    expect(body('.rd-h .ql-hint', true)).toMatch(/display:\s*none/)
    expect(body('.rd-tools .rd-ext', true)).toMatch(/width:\s*var\(--size-control\)/)
    expect(body('.rd-h', true)).toMatch(/grid-template-areas/)
    expect(body('.rd-sub', true)).toMatch(/grid-area:\s*s/)
    expect(body('.rd-sub', true)).not.toMatch(/display:\s*none/)
    // 44 px above 480 px, unchanged
    expect(body('.rd-h', false)).toMatch(/min-height:\s*var\(--size-row-2\)/)
  })
})

describe('the old-version band (verify lot 3)', () => {
  it('« Version ancienne · **t19**, 4 tours avant la dernière. »: the round in bold, a full stop', async () => {
    const { dlg } = await openReader()
    for (let i = 0; i < 4; i += 1) fireEvent.keyDown(document.body, { key: 'ArrowLeft' })
    const band = dlg.querySelector('.rd-old') as HTMLElement
    const text = band.querySelector('.rd-old-t') as HTMLElement
    expect(text.textContent).toBe('Version ancienne · t19, 4 tours avant la dernière.')
    expect(text.querySelector('b')).toHaveTextContent('t19')
  })

  it('its button never wraps; only the text may', () => {
    const css = readFileSync(join(process.cwd(), 'src/reader/reader.css'), 'utf8')
    const of = (sel: string) =>
      blocks(css)
        .filter((b) => b.selector.replace(/^@layer \w+ /, '').trim() === sel)
        .map((b) => b.body)
        .join(';')
    expect(of('.rd-old .ql-z')).toMatch(/white-space:\s*nowrap/)
    expect(of('.rd-old .ql-z')).toMatch(/flex:\s*none/)
    expect(of('.rd-old-t')).toMatch(/min-width:\s*0/)
  })
})
