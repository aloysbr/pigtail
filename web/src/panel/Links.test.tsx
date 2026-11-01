import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, fireEvent, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { Output, OutputGroup, OutputTour } from '@/api/types'
import { makeBacklog } from '@/test/backlog'
import { getUi, setUi } from '@/state/ui'
import { groupButton, renderApp } from '@/test/render'
import { blocks } from '@/theme/css-parse'
import { noteHref, safeHref } from './Links'

const VAULT = 'Perso/Notes/pigtail/subjects/atlas web/index.md'

function out(p: Partial<Output> & { kind: string }): Output {
  return { seq: 1, id: `out_${p.kind}${p.title ?? ''}`, subject_id: 'pig-r1', key: `k:${p.kind}:${p.title ?? ''}`, title: '', ts: 0, ...p }
}

async function openWith(outputs: Output[], vault?: string, groups: OutputGroup[] = []) {
  const data = makeBacklog()
  data.subjects = data.subjects.map((s) => (s.id === 'pig-r1' ? { ...s, vault_path: vault } : s))
  data.outputs = { 'pig-r1': outputs }
  data.groups = { 'pig-r1': groups }
  const r = await renderApp(data)
  await r.user.click(groupButton('th:th_pig'))
  await r.user.click(document.querySelector('li[data-id="pig-r1"]') as HTMLElement)
  const sec = document.querySelector<HTMLElement>('section[aria-labelledby="ln-h"]')
  if (!sec) throw new Error('no Liens section')
  return { ...r, sec }
}

describe('safeHref', () => {
  it('keeps http, https and obsidian links only', () => {
    expect(safeHref('https://github.com/a/b/pull/1')).toBe('https://github.com/a/b/pull/1')
    expect(safeHref('http://127.0.0.1:7800/x')).toBe('http://127.0.0.1:7800/x')
    expect(safeHref('obsidian://open?path=a')).toBe('obsidian://open?path=a')
    for (const bad of ['javascript:alert(1)', ' JavaScript:alert(1)', 'data:text/html,<b>x</b>', 'file:///etc/passwd', 'vbscript:x', '/relative', '']) {
      expect(safeHref(bad)).toBeNull()
    }
  })

  it('the note link is obsidian://open?path= + the encoded vault path', () => {
    expect(noteHref(VAULT)).toBe(`obsidian://open?path=${encodeURIComponent(VAULT)}`)
  })
})

describe('Links', () => {
  it('« Note » first: obsidian://open?path=<vault_path encodé>', async () => {
    const { sec } = await openWith([], VAULT)
    const items = sec.querySelectorAll('li')
    expect(items[0].querySelector('.chip')).toHaveTextContent('Note')
    expect(within(items[0]).getByRole('link')).toHaveAttribute('href', `obsidian://open?path=${encodeURIComponent(VAULT)}`)
  })

  it('then each output with its type chip', async () => {
    const { sec } = await openWith(
      [out({ kind: 'pr', url: 'https://github.com/aloysbr/pigtail/pull/250', title: 'PR #250' }), out({ kind: 'mockup', url: 'http://127.0.0.1:7900/t10/', title: 'Maquette t10' }), out({ kind: 'report', path: '/Users/x/_outputs/r/index.html', title: 'Rapport lot 2' })],
      VAULT,
    )
    const chips = [...sec.querySelectorAll('li .chip')].map((c) => c.textContent)
    expect(chips).toEqual(['Note', 'PR', 'Maquette', 'Rapport'])
    expect(within(sec).getByRole('link', { name: 'PR #250' })).toHaveAttribute('href', 'https://github.com/aloysbr/pigtail/pull/250')
    // a path is not a browser link: plain text
    expect(within(sec).queryByRole('link', { name: 'Rapport lot 2' })).toBeNull()
    expect(sec).toHaveTextContent('Rapport lot 2')
  })

  it('a javascript: or data: output is never clickable: plain text', async () => {
    const { sec } = await openWith([out({ kind: 'link', url: 'javascript:alert(1)', title: 'Piège' }), out({ kind: 'html', url: 'data:text/html,<script>x</script>', title: 'Données' })])
    expect(sec).toHaveTextContent('Piège')
    expect(sec).toHaveTextContent('Données')
    expect(sec.querySelectorAll('a').length).toBe(0)
  })

  it('a purged output is not listed', async () => {
    const { sec } = await openWith([out({ kind: 'pr', url: 'https://x.example/1', title: 'Vieille PR', purged_at: 5 })])
    expect(sec).not.toHaveTextContent('Vieille PR')
  })

  it('a subject with no deliverable group renders the same DOM as before the groups (snapshot taken on the base)', async () => {
    const { sec } = await openWith(
      [
        out({ seq: 1, kind: 'pr', url: 'https://github.com/aloysbr/pigtail/pull/250', title: 'PR #250' }),
        out({ seq: 2, kind: 'capture', url: 'https://x.example/shots/desktop.png', title: 'desktop.png' }),
        out({ seq: 3, kind: 'report', path: '/Users/x/_outputs/r/index.html', title: 'Rapport lot 2' }),
      ],
      VAULT,
    )
    expect(sec.outerHTML).toMatchInlineSnapshot(`"<section class="lp-sec" aria-labelledby="ln-h"><h3 class="lp-h3" id="ln-h">Liens<span class="lp-hint" aria-hidden="true"><kbd>1</kbd>–<kbd>3</kbd> ouvre</span></h3><ul class="links"><li><span class="chip">Note</span><a class="lnk" href="obsidian://open?path=Perso%2FNotes%2Fpigtail%2Fsubjects%2Fatlas%20web%2Findex.md" title="Perso/Notes/pigtail/subjects/atlas web/index.md" aria-keyshortcuts="1">index.md</a><kbd class="lk-k" aria-hidden="true">1</kbd></li><li><span class="chip">PR</span><a class="lnk" href="https://github.com/aloysbr/pigtail/pull/250" title="https://github.com/aloysbr/pigtail/pull/250" target="_blank" rel="noopener noreferrer" aria-keyshortcuts="2">PR #250</a><kbd class="lk-k" aria-hidden="true">2</kbd></li><li><span class="chip">Capture d'écran</span><a class="lnk" href="https://x.example/shots/desktop.png" title="https://x.example/shots/desktop.png" target="_blank" rel="noopener noreferrer" aria-keyshortcuts="3" data-img="out_capturedesktop.png">desktop.png</a><kbd class="lk-k" aria-hidden="true">3</kbd></li><li><span class="chip">Rapport</span><span class="lnk-t" title="/Users/x/_outputs/r/index.html">Rapport lot 2</span></li></ul></section>"`)
  })

  it('no note and no output: « Aucun lien : la PR et les notes du vault apparaîtront ici. »', async () => {
    const { sec } = await openWith([])
    expect(sec).toHaveTextContent('Aucun lien : la PR et les notes du vault apparaîtront ici.')
  })
})

/** A physical digit key pressed on the page (AZERTY: key « é » for Digit2), on `target` or the body. */
function press(code: string, key: string, target: Element = document.body, init: KeyboardEventInit = {}) {
  fireEvent.keyDown(target, { code, key, ...init })
}

/** 11 http outputs, « L1 » … « L11 ». */
function eleven(): Output[] {
  return Array.from({ length: 11 }, (_, i) => ({ ...out({ kind: 'link', url: `https://x.example/${i + 1}`, title: `L${i + 1}` }), id: `out_l${i + 1}`, seq: i + 1 }))
}

describe('Liens: digits 1-9 open a link by the physical key', () => {
  it('11 links: only the first 9 carry a digit; the hint « 1–9 ouvre » sits right of the title', async () => {
    const { sec } = await openWith(eleven())
    const digits = [...sec.querySelectorAll('li')].map((li) => li.querySelector('kbd')?.textContent ?? '')
    expect(digits).toEqual(['1', '2', '3', '4', '5', '6', '7', '8', '9', '', ''])
    expect(sec.querySelector('h3 .lp-hint')?.textContent).toBe('1–9 ouvre')
    expect(within(sec).getByRole('link', { name: 'L2' })).toHaveAttribute('aria-keyshortcuts', '2')
  })

  it('pressing 2 (AZERTY « é ») opens the 2nd link: window.open for an http link', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openWith(eleven())
    press('Digit2', 'é')
    expect(open).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledWith('https://x.example/2', '_blank', 'noopener,noreferrer')
  })

  it('an obsidian:// link opens through an anchor, never window.open', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const clicked: string[] = []
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this.href)
    })
    await openWith([], VAULT)
    press('Digit1', '&')
    expect(open).not.toHaveBeenCalled()
    expect(clicked).toEqual([`obsidian://open?path=${encodeURIComponent(VAULT)}`])
  })

  it('a link with no safe href carries no digit, and the digits skip it', async () => {
    const { sec } = await openWith([out({ kind: 'report', path: '/Users/x/r.html', title: 'Local' }), out({ kind: 'pr', url: 'https://x.example/pr', title: 'PR' })])
    const rows = [...sec.querySelectorAll('li')].map((li) => [li.textContent?.includes('Local') ? 'Local' : 'PR', li.querySelector('kbd')?.textContent ?? ''])
    expect(rows).toEqual([['Local', ''], ['PR', '1']])
    expect(sec.querySelector('h3 .lp-hint')?.textContent).toBe('1 ouvre')
  })

  it('in a field, nothing opens', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openWith(eleven())
    const input = document.createElement('input')
    document.body.append(input)
    press('Digit2', 'é', input)
    expect(open).not.toHaveBeenCalled()
    input.remove()
  })

  it("during `g` (menu 'go'), the digit belongs to the areas: nothing opens", async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openWith(eleven())
    act(() => setUi({ menu: 'go' }))
    press('Digit2', 'é')
    expect(open).not.toHaveBeenCalled()
  })

  it('palette or Réglages open: nothing opens', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openWith(eleven())
    act(() => setUi({ settings: true }))
    press('Digit2', 'é')
    act(() => setUi({ settings: false, palette: true }))
    press('Digit2', 'é')
    expect(open).not.toHaveBeenCalled()
  })

  it('no border nor rule between the lines (t4: « pas de filets »)', () => {
    const css = readFileSync(join(process.cwd(), 'src/panel/panel.css'), 'utf8')
    const li = blocks(css).filter((b) => /(^|\s)\.links li\b/.test(b.selector.replace(/^@layer \w+ /, '')))
    expect(li.length).toBeGreaterThan(0)
    for (const b of li) expect(b.body).not.toMatch(/border|box-shadow/)
  })
})

const ROOT = '/Users/x/_outputs/app-macos-atlas/mockup'

/** The 21 rounds t3…t23 of one mockup, registered as outputs out_t3…out_t23 (seq 3…23), the last one yesterday 15:11. */
function rounds(missing: number[] = []): { outputs: Output[]; group: OutputGroup } {
  const yesterday = new Date()
  yesterday.setDate(yesterday.getDate() - 1)
  yesterday.setHours(15, 11, 0, 0)
  const tours: OutputTour[] = []
  const outputs: Output[] = []
  for (let n = 3; n <= 23; n += 1) {
    const ts = yesterday.getTime() - (23 - n) * 3_600_000
    const path = `${ROOT}/t${n}/index.html`
    const served = `http://127.0.0.1:7801${ROOT}/t${n}/`
    const exists = !missing.includes(n)
    outputs.push({ seq: n, id: `out_t${n}`, subject_id: 'pig-r1', kind: 'mockup', key: path, path, title: `mockup t${n}`, ts, exists, served_url: exists ? served : undefined })
    tours.push({ n, label: `t${n}`, output_id: `out_t${n}`, title: `mockup t${n}`, ts, path, exists, served_url: exists ? served : undefined })
  }
  return { outputs, group: { key: ROOT, name: 'app-macos-atlas', kind: 'mockup', first_seq: 3, tours } }
}

describe('Liens: one line per deliverable (D2)', () => {
  it('21 rounds sit on one line right under the Note: chip, the group name, « 21 tours · t23, hier 15:11 »', async () => {
    const r = rounds()
    const pr = out({ seq: 30, kind: 'pr', url: 'https://github.com/aloysbr/pigtail/pull/250', title: 'PR #250' })
    const { sec } = await openWith([...r.outputs, pr], VAULT, [r.group])
    const lis = [...sec.querySelectorAll('li')]
    expect(lis.length).toBe(3)
    expect(lis.map((li) => li.querySelector('.chip')?.textContent)).toEqual(['Note', 'Maquette', 'PR'])
    const btn = within(lis[1]).getByRole('button', { name: 'app-macos-atlas' })
    expect(btn).toHaveClass('lnk-b')
    expect(btn).toHaveAttribute('data-group', ROOT)
    expect(lis[1].querySelector('.lk-n')?.textContent).toBe('21 tours · t23, hier 15:11')
    // the existing digit: Note 1, the deliverable 2, the PR 3
    expect(lis[1].querySelector('kbd.lk-k')?.textContent).toBe('2')
  })

  it('a click, Space and Enter open the reader on the latest version', async () => {
    const r = rounds()
    const { sec, user } = await openWith(r.outputs, VAULT, [r.group])
    const btn = within(sec).getByRole('button', { name: 'app-macos-atlas' })
    await user.click(btn)
    expect(getUi().reader).toEqual({ subjectId: 'pig-r1', groupKey: ROOT, tour: null })
    for (const key of [' ', '{Enter}']) {
      act(() => setUi({ reader: null }))
      btn.focus()
      await user.keyboard(key)
      expect(getUi().reader).toEqual({ subjectId: 'pig-r1', groupKey: ROOT, tour: null })
    }
  })

  it('its digit opens the reader too, never a tab', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const r = rounds()
    await openWith(r.outputs, VAULT, [r.group])
    press('Digit2', 'é')
    expect(open).not.toHaveBeenCalled()
    expect(getUi().reader).toEqual({ subjectId: 'pig-r1', groupKey: ROOT, tour: null })
  })

  it('a round gone from the disk is counted: « 21 tours · … · 1 introuvable »', async () => {
    const r = rounds([7])
    const { sec } = await openWith(r.outputs, VAULT, [r.group])
    expect(sec.querySelector('.lk-n')?.textContent).toBe('21 tours · t23, hier 15:11 · 1 introuvable')
  })

  it('a capture with served_url is a link to the preview, and a capture with a path only becomes one (src = served_url)', async () => {
    const served = 'http://127.0.0.1:7801/Users/x/_outputs/s/desktop.png'
    const { sec, user } = await openWith([
      out({ seq: 1, kind: 'capture', path: '/Users/x/_outputs/s/desktop.png', served_url: served, exists: true, title: 'desktop.png' }),
    ])
    const a = within(sec).getByRole('link', { name: 'desktop.png' })
    expect(a).toHaveAttribute('href', served)
    await user.click(a)
    expect(getUi().preview).toEqual({ subjectId: 'pig-r1', index: 0 })
    const img = await within(document.body).findByRole('img', { name: /desktop\.png/ })
    expect(img).toHaveAttribute('src', served)
  })

  it('a pdf deliverable opens its tab (a link to served_url), never the reader', async () => {
    const served = 'http://127.0.0.1:7801/Users/x/_outputs/r/lot.pdf'
    const { sec, user } = await openWith([out({ seq: 1, kind: 'pdf', path: '/Users/x/_outputs/r/lot.pdf', served_url: served, exists: true, title: 'lot.pdf' })])
    const a = within(sec).getByRole('link', { name: 'lot.pdf' })
    expect(a).toHaveAttribute('href', served)
    expect(a).toHaveAttribute('target', '_blank')
    act(() => setUi({ reader: null }))
    a.addEventListener('click', (e) => e.preventDefault())
    await user.click(a)
    expect(getUi().reader).toBeNull()
  })
})
