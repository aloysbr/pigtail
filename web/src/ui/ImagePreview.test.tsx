import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { Output } from '@/api/types'
import { getUi, setUi } from '@/state/ui'
import { makeBacklog } from '@/test/backlog'
import { groupButton, renderApp } from '@/test/render'
import { blocks } from '@/theme/css-parse'

function out(seq: number, p: Partial<Output> & { kind: string }): Output {
  return { seq, id: `out_${seq}`, subject_id: 'pig-r1', key: `k${seq}`, title: '', ts: 0, ...p }
}

/** D13 « dépend du sujet moteur » (D15): proven on http images only. Links 1-4: a page, then three images; a local capture last (no digit). */
const OUTPUTS: Output[] = [
  out(1, { kind: 'link', url: 'https://x.example/doc', title: 'Doc' }),
  out(2, { kind: 'capture', url: 'https://x.example/shots/one.png', title: 'Capture une' }),
  out(3, { kind: 'file', url: 'https://x.example/two.jpg', title: 'Deux' }),
  out(4, { kind: 'capture', url: 'https://x.example/three', title: 'Trois' }),
  out(5, { kind: 'capture', path: '/Users/x/_outputs/local.png', title: 'Locale' }),
]

async function openSheet() {
  const data = makeBacklog()
  data.outputs = { 'pig-r1': OUTPUTS }
  const r = await renderApp(data)
  await r.user.click(groupButton('th:th_pig'))
  await r.user.click(document.querySelector('li[data-id="pig-r1"]') as HTMLElement)
  await screen.findByRole('link', { name: 'Trois' })
  return r
}

const dlg = () => screen.queryByRole('dialog', { name: /^Aperçu de / })
const key = (k: string, init: Partial<KeyboardEventInit> = {}) => fireEvent.keyDown(document.activeElement ?? document.body, { key: k, ...init })

describe('Image preview (Quick Look, D13 on http fixtures; D15)', () => {
  it('the digit opens the preview on that image, never window.open; « 2 / 3 » and the output title (as the link and the foot) in the head', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit3', key: '"' })
    const d = dlg()
    expect(d).not.toBeNull()
    expect(d).toHaveAttribute('aria-modal', 'true')
    expect((d as HTMLElement).querySelector('.ql-n b')).toHaveTextContent('Deux')
    expect(d).toHaveAccessibleName('Aperçu de Deux')
    expect(within(d as HTMLElement).getByText('2 / 3')).toBeInTheDocument()
    expect(open).not.toHaveBeenCalled()
  })

  it('a click on an image link opens the preview, without following the link', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openSheet()
    const link = screen.getByRole('link', { name: 'Capture une' })
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true })
    act(() => {
      link.dispatchEvent(ev)
    })
    expect(ev.defaultPrevented).toBe(true)
    expect(within(dlg() as HTMLElement).getByText('1 / 3')).toBeInTheDocument()
    expect(open).not.toHaveBeenCalled()
  })

  it('Space on the focused image link opens it; Space again closes it', async () => {
    await openSheet()
    const link = screen.getByRole('link', { name: 'Trois' })
    act(() => link.focus())
    key(' ', { code: 'Space' })
    expect(within(dlg() as HTMLElement).getByText('3 / 3')).toBeInTheDocument()
    act(() => (document.body as HTMLElement).focus())
    key(' ', { code: 'Space' })
    expect(dlg()).toBeNull()
  })

  it('the page link and the local capture are no image links: no preview, the local one stays text', async () => {
    await openSheet()
    expect(screen.queryByRole('link', { name: 'Locale' })).toBeNull()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    fireEvent.keyDown(document.body, { code: 'Digit1', key: '&' })
    expect(dlg()).toBeNull()
    expect(open).toHaveBeenCalledWith('https://x.example/doc', '_blank', 'noopener,noreferrer')
  })

  it('a preview with no image link clears itself, so the keys are not swallowed for nothing', async () => {
    await openSheet() // sheet of pig-r1, which has images
    act(() => setUi({ preview: { subjectId: 'pig-r2', index: 0 } })) // a subject without outputs
    await vi.waitFor(() => expect(getUi().preview).toBeNull())
  })

  it('→ goes to the next image, disabled on the last; ← back, disabled on the first', async () => {
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    const d = () => dlg() as HTMLElement
    expect(within(d()).getByRole('button', { name: /Image précédente/ })).toHaveAttribute('aria-disabled', 'true')
    key('ArrowRight')
    expect(within(d()).getByText('2 / 3')).toBeInTheDocument()
    key('ArrowRight')
    expect(within(d()).getByText('3 / 3')).toBeInTheDocument()
    expect(within(d()).getByRole('button', { name: /Image suivante/ })).toHaveAttribute('aria-disabled', 'true')
    key('ArrowRight')
    expect(within(d()).getByText('3 / 3')).toBeInTheDocument()
    fireEvent.click(within(d()).getByRole('button', { name: /Image précédente/ }))
    expect(within(d()).getByText('2 / 3')).toBeInTheDocument()
  })

  it('z, or a click on the image, toggles « Taille réelle » (aria-pressed)', async () => {
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    const zb = within(dlg() as HTMLElement).getByRole('button', { name: /Taille réelle/ })
    expect(zb).toHaveAttribute('aria-pressed', 'false')
    key('z')
    expect(zb).toHaveAttribute('aria-pressed', 'true')
    expect(document.querySelector('.ql')).toHaveClass('real')
    fireEvent.click(document.querySelector('.ql-img') as HTMLElement)
    expect(zb).toHaveAttribute('aria-pressed', 'false')
  })

  it('the footer: caption, natural size once loaded, « ajustée »; no weight without one from the daemon', async () => {
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    const img = document.querySelector('.ql-img') as HTMLImageElement
    expect(img).toHaveAttribute('src', 'https://x.example/shots/one.png')
    Object.defineProperty(img, 'naturalWidth', { value: 1280 })
    Object.defineProperty(img, 'naturalHeight', { value: 3617 })
    fireEvent.load(img)
    const foot = document.querySelector('.ql-f') as HTMLElement
    expect(foot).toHaveTextContent('Capture une')
    expect(foot).toHaveTextContent('1280 × 3617')
    expect(foot).toHaveTextContent(/ajustée/)
    expect(foot).not.toHaveTextContent(/Ko|Mo/)
  })

  it('Esc closes and gives the focus back to the link of the last image seen', async () => {
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    key('ArrowRight')
    key('ArrowRight')
    key('Escape')
    expect(dlg()).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('link', { name: 'Trois' }))
  })

  it('Esc stacked: the preview first, the sheet stays; a second Esc closes the sheet', async () => {
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    key('Escape')
    expect(dlg()).toBeNull()
    expect(getUi().panel).toBe(true)
    key('Escape')
    expect(getUi().panel).toBe(false)
  })

  it('h (Standby) does nothing while the preview is open', async () => {
    const r = await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    const writes = r.writes().length
    key('h')
    expect(r.writes()).toHaveLength(writes)
    expect(dlg()).not.toBeNull()
  })

  it('a failed load says « Image indisponible. »; Esc still closes', async () => {
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    fireEvent.error(document.querySelector('.ql-img') as HTMLElement)
    expect(within(dlg() as HTMLElement).getByText('Image indisponible.')).toBeInTheDocument()
    key('Escape')
    expect(dlg()).toBeNull()
  })
})

describe('Image preview focus containment', () => {
  it('Tab and Shift+Tab cycle inside the preview; the page behind is inert', async () => {
    await openSheet()
    fireEvent.keyDown(document.body, { code: 'Digit2', key: 'é' })
    const d = dlg() as HTMLElement
    const tabbables = [...d.querySelectorAll<HTMLElement>('button')]
    const first = tabbables[0]
    const last = tabbables[tabbables.length - 1]
    act(() => last.focus())
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(first)
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)
    // focus on the window itself (just opened): Tab lands inside
    act(() => d.focus())
    fireEvent.keyDown(d, { key: 'Tab' })
    expect(d.contains(document.activeElement)).toBe(true)
    expect(document.querySelector('.lpanel')?.closest('[inert]')).not.toBeNull()
  })
})

describe('preview.css', () => {
  const read = (p: string) => blocks(readFileSync(join(process.cwd(), p), 'utf8'))
  const strip = (s: string) => s.replace(/^@layer \w+ /, '')
  const rule = (bs: ReturnType<typeof blocks>, sel: RegExp) => {
    const b = bs.find((x) => sel.test(strip(x.selector)))
    if (!b) throw new Error(`no rule ${sel}`)
    return b.body
  }
  const z = (body: string) => Number(/z-index:\s*(-?\d+)/.exec(body)?.[1] ?? NaN)
  const pv = read('src/ui/preview.css')

  it('stacks over the sheet, the Réglages popover and the palette (z-index read)', () => {
    const sheet = z(rule(read('src/app/layout.css'), /\.pm-over > \.pwrap$/))
    const pal = z(rule(read('src/app/layout.css'), /^\.palette$/))
    const pop = z(rule(read('src/ui/settings.css'), /^\.pop$/))
    const veil = z(rule(pv, /^\.ql-veil$/))
    const win = z(rule(pv, /^\.ql$/))
    for (const under of [sheet, pal, pop]) expect(veil).toBeGreaterThan(under)
    expect(win).toBeGreaterThan(veil)
  })

  it('the veil is --veil; the window reads --size-ql-w and --size-ql-min, the side buttons --size-ql-nav', () => {
    expect(rule(pv, /^\.ql-veil$/)).toMatch(/background:\s*var\(--veil\)/)
    expect(rule(pv, /^\.ql$/)).toMatch(/var\(--size-ql-w\)/)
    expect(rule(pv, /^\.ql$/)).toMatch(/var\(--size-ql-min\)/)
    expect(rule(pv, /^\.ql-nav$/)).toMatch(/var\(--size-ql-nav\)/)
  })

  it('a very tall image stays in the window (bounded max-height); in real size it scrolls inside', () => {
    expect(rule(pv, /^\.ql-img$/)).toMatch(/max-height:\s*100%/)
    expect(rule(pv, /^\.ql\.real \.ql-img$/)).toMatch(/max-height:\s*none/)
    expect(rule(pv, /^\.ql\.real \.ql-b$/)).toMatch(/overflow:\s*auto/)
  })

  it('a fitted image has a hairline and a shadow so it never melts into the stage (tokens only)', () => {
    const b = rule(pv, /^\.ql-img$/)
    expect(b).toMatch(/box-shadow:\s*0 0 0 var\(--size-bw\) var\(--border-strong\), var\(--shadow-pop\)/)
  })

  it('on a phone the stage reserves side margins wider than the ← → buttons, so they stay off the image', () => {
    const b = rule(pv, /^\.phone \.ql-b$/)
    expect(b).toMatch(/padding-inline:\s*calc\(var\(--size-touch\) \+ 2 \* var\(--sp-4\)\)/)
  })

  it('opens in a --dur-fast fade, nothing under reduced motion (G4)', () => {
    const fade = pv.filter((b) => /animation:/.test(b.body))
    expect(fade.length).toBeGreaterThan(0)
    for (const b of fade) {
      expect(b.selector).toMatch(/prefers-reduced-motion: no-preference/)
      expect(b.body).toMatch(/var\(--dur-fast\)/)
    }
  })

  it('globals.css imports preview.css (before pointer.css)', () => {
    const g = readFileSync(join(process.cwd(), 'src/theme/globals.css'), 'utf8')
    expect(g.indexOf('@import "../ui/preview.css"')).toBeGreaterThan(0)
    expect(g.indexOf('@import "../ui/preview.css"')).toBeLessThan(g.indexOf('@import "./pointer.css"'))
  })
})
