import { fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { getUi } from '@/state/ui'
import { renderApp, rowIds, unfoldAll } from '@/test/render'

function side() {
  return within(screen.getByRole('complementary', { name: 'Navigation' }))
}
function areaItem(name: string) {
  return side().getByRole('button', { name: new RegExp(`^${name}`) })
}

describe('Sidebar', () => {
  it('picking an area clears the project filter: one chip, one aria-current', async () => {
    await renderApp()
    unfoldAll()
    const proj = side().getAllByRole('button').find((b) => b.classList.contains('nav-proj'))!
    fireEvent.click(proj)
    expect(getUi().projectF).not.toBeNull()
    fireEvent.click(areaItem('Vineyard'))
    expect(getUi().projectF).toBeNull()
    expect(getUi().areaF?.name).toBe('Vineyard')
    expect(document.querySelectorAll('aside [aria-current]')).toHaveLength(1)
  })

  it('g shows 1…9 on the area entries, gone after the digit', async () => {
    await renderApp()
    const kbds = () => [...document.querySelectorAll('aside .nav-i:not(.nav-proj)')].map((b) => b.querySelector('kbd')?.textContent ?? null)
    expect(kbds().every((k) => k === null)).toBe(true)
    fireEvent.keyDown(document.body, { key: 'g' })
    // Sessions and Backlog have none; areas run 1…n across both scopes (globex's first area is not 1 again)
    expect(kbds()).toEqual([null, null, '1', '2', '3', '4', '5'])
    fireEvent.keyDown(document.body, { key: '2', code: 'Digit2' })
    expect(kbds().every((k) => k === null)).toBe(true)
  })
  it('shows « Pigtail », the Backlog total, each scope title and its areas with their open counts', async () => {
    await renderApp()
    expect(side().getByText('Pigtail', { selector: '.brand b' })).toBeInTheDocument()
    expect(side().getByRole('button', { name: /Backlog/ })).toHaveTextContent('18')
    expect(side().getByText('perso', { selector: '.nav-g' })).toBeInTheDocument()
    expect(side().getByText('globex', { selector: '.nav-g' })).toBeInTheDocument()
    expect(areaItem('Allotment')).toHaveTextContent('11') // 13 shown, 2 done: open count like the project rows
    expect(areaItem('Garden')).toHaveTextContent('1') // 2 shown, 1 done
    expect(areaItem('Vide')).toHaveTextContent('0')
    expect(areaItem('Vineyard')).toHaveTextContent('2')
  })

  it('« Sans aire » is in the sidebar with its count and filters like any area', async () => {
    const { user } = await renderApp()
    expect(areaItem('Sans aire')).toHaveTextContent('1')
    await user.click(areaItem('Sans aire'))
    unfoldAll()
    expect(rowIds()).toEqual(['orphan'])
    expect(areaItem('Sans aire')).toHaveAttribute('aria-current', 'true')
  })

  it('a click on an area filters the list, with an « Aire X » chip; its count = its visible rows', async () => {
    const { user } = await renderApp()
    await user.click(areaItem('Garden'))
    expect(screen.getByText('Aire').closest('.fchip')).toHaveTextContent('Garden')
    unfoldAll()
    expect(rowIds().sort()).toEqual(['watering-1', 'watering-d'])
    expect(areaItem('Garden')).toHaveAttribute('aria-current', 'true')
  })

  it('D16: each project of an area is a row with its Pie, its name and its open count; a click filters the backlog on it', async () => {
    const { user } = await renderApp()
    const proj = (name: string) => side().getByRole('button', { name: new RegExp(`^\\d+ % fait ${name}`) })
    // pigtail: 12 subjects shown (dropped and captured hidden), 2 done → 17 %, 10 open
    expect(proj('pigtail')).toHaveTextContent(/pigtail\s*10/)
    expect(proj('pigtail').querySelector('svg[role="img"]')).not.toBeNull()
    expect(proj('Outils')).toHaveTextContent('1')
    await user.click(proj('pigtail'))
    unfoldAll()
    expect(rowIds().every((id) => id.startsWith('pig-'))).toBe(true)
    expect(rowIds()).toContain('pig-r1')
    expect(proj('pigtail')).toHaveAttribute('aria-current', 'true')
    expect(areaItem('Allotment')).not.toHaveAttribute('aria-current')
    expect(screen.getByText('Projet').closest('.fchip')).toHaveTextContent('pigtail')
    // no project sheet: the panel stays closed
    expect(getUi().panel).toBe(false)
    // a second click clears it, like an area
    await user.click(proj('pigtail'))
    expect(getUi().projectF).toBeNull()
  })

  it('one meaning: an area shows its OPEN count (done excluded), the sum of its project rows', async () => {
    await renderApp()
    const items = [...document.querySelectorAll('aside .nav .nav-i')]
    const num = (el: Element) => Number((el.querySelector('.meta')?.textContent ?? '').replace(/\s/g, ''))
    let checked = 0
    let sum = 0
    let area: Element | null = null
    const close = () => {
      if (area) {
        expect(num(area), `${area.querySelector('.nav-l')?.textContent} = sum of its projects' open`).toBe(sum)
        checked += 1
      }
    }
    for (const el of items) {
      if (el.classList.contains('nav-area')) {
        close()
        area = el
        sum = 0
      } else if (el.classList.contains('nav-proj')) sum += num(el)
    }
    close()
    expect(checked).toBeGreaterThan(2)
  })

  it('the ⌘K button opens the palette', async () => {
    const { user } = await renderApp()
    await user.click(side().getByRole('button', { name: /Palette de commandes/ }))
    expect(getUi().palette).toBe(true)
  })

  it('under 1 120 px the sidebar floats: ⌘B (button and key) opens and closes it', async () => {
    const { user } = await renderApp(undefined, { width: 1024 })
    expect(screen.queryByRole('complementary', { name: 'Navigation' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Navigation' }))
    expect(screen.getByRole('complementary', { name: 'Navigation' })).toHaveClass('float')
    fireEvent.keyDown(document.body, { key: 'b', code: 'KeyB', metaKey: true })
    expect(screen.queryByRole('complementary', { name: 'Navigation' })).toBeNull()
    fireEvent.keyDown(document.body, { key: 'b', code: 'KeyB', metaKey: true })
    expect(screen.getByRole('complementary', { name: 'Navigation' })).toBeInTheDocument()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(screen.queryByRole('complementary', { name: 'Navigation' })).toBeNull()
  })

  it('docked at 1 440 px; ⌘B puts it away', async () => {
    await renderApp()
    expect(screen.getByRole('complementary', { name: 'Navigation' })).not.toHaveClass('float')
    fireEvent.keyDown(document.body, { key: 'b', code: 'KeyB', metaKey: true })
    expect(screen.queryByRole('complementary', { name: 'Navigation' })).toBeNull()
  })

  it('wide screen: ⌘B puts the sidebar away, the next ⌘B docks it again (never a floating one)', async () => {
    await renderApp()
    fireEvent.keyDown(document.body, { key: 'b', code: 'KeyB', metaKey: true })
    expect(screen.queryByRole('complementary', { name: 'Navigation' })).toBeNull()
    fireEvent.keyDown(document.body, { key: 'b', code: 'KeyB', metaKey: true })
    expect(screen.getByRole('complementary', { name: 'Navigation' })).not.toHaveClass('float')
  })

  it('wide screen, sidebar put away: the Navigation button docks it again, not a float', async () => {
    const { user } = await renderApp()
    fireEvent.keyDown(document.body, { key: 'b', code: 'KeyB', metaKey: true })
    await user.click(screen.getByRole('button', { name: 'Navigation' }))
    expect(screen.getByRole('complementary', { name: 'Navigation' })).not.toHaveClass('float')
  })

  it('header line: « Pigtail » carries a ⌘K icon button (one key) and a ⌘B button, no separate search row', async () => {
    const { user } = await renderApp()
    const brand = document.querySelector('.side .brand') as HTMLElement
    const k = within(brand).getByRole('button', { name: /Palette de commandes/ })
    expect([...k.querySelectorAll('kbd')].map((x) => x.textContent)).toEqual(['⌘K'])
    expect(side().queryByText('Rechercher…')).toBeNull()
    const b = within(brand).getByRole('button', { name: 'Ranger la barre latérale' })
    expect([...b.querySelectorAll('kbd')].map((x) => x.textContent)).toEqual(['⌘B'])
    await user.click(k)
    expect(getUi().palette).toBe(true)
    await user.click(b)
    expect(screen.queryByRole('complementary', { name: 'Navigation' })).toBeNull()
  })

  it('floating sidebar, 800 px and up: « Ancrer » docks it (t10 data-sidedock); no ⌘B « Ranger » there', async () => {
    const { user } = await renderApp(undefined, { width: 1024 })
    await user.click(screen.getByRole('button', { name: 'Navigation' }))
    const aside = screen.getByRole('complementary', { name: 'Navigation' })
    expect(aside).toHaveClass('float')
    expect(within(aside).queryByRole('button', { name: /Ranger/ })).toBeNull()
    await user.click(within(aside).getByRole('button', { name: 'Ancrer la barre latérale' }))
    const docked = screen.getByRole('complementary', { name: 'Navigation' })
    expect(docked).not.toHaveClass('float')
    expect(within(docked).getByRole('button', { name: 'Ranger la barre latérale' })).toBeInTheDocument()
    expect(document.querySelector('.side-scrim')).toBeNull()
    expect(document.querySelector('.app')).not.toHaveClass('nodock')
  })

  it('floating sidebar under 800 px: no « Ancrer »', async () => {
    const { user } = await renderApp(undefined, { width: 700 })
    await user.click(screen.getByRole('button', { name: 'Navigation' }))
    expect(within(screen.getByRole('complementary', { name: 'Navigation' })).queryByRole('button', { name: /Ancrer/ })).toBeNull()
  })

  it('on the Sessions view only « Sessions » is current, even with a project filter set; the value is the Backlog’s (« true »)', async () => {
    await renderApp()
    unfoldAll()
    const proj = side().getAllByRole('button').find((b) => b.classList.contains('nav-proj'))!
    fireEvent.click(proj)
    expect(getUi().projectF).not.toBeNull()
    fireEvent.keyDown(document.body, { key: 's' })
    expect(getUi().view).toBe('sessions')
    const cur = [...document.querySelectorAll('aside [aria-current]')]
    expect(cur).toHaveLength(1)
    expect(cur[0]).toHaveTextContent(/^Sessions/)
    expect(cur[0]).toHaveAttribute('aria-current', 'true')
    expect(document.querySelectorAll('aside .nav-i.cur')).toHaveLength(1)
  })

  it('a click on the already filtered project from the Sessions view returns to the backlog with that filter kept (no toggle-off)', async () => {
    await renderApp()
    unfoldAll()
    const proj = () => side().getAllByRole('button').find((b) => b.classList.contains('nav-proj'))!
    fireEvent.click(proj())
    const key = getUi().projectF
    expect(key).not.toBeNull()
    fireEvent.keyDown(document.body, { key: 's' })
    fireEvent.click(proj())
    expect(getUi()).toMatchObject({ view: 'backlog', projectF: key })
    // back in the backlog, the same click is the usual toggle-off
    fireEvent.click(proj())
    expect(getUi().projectF).toBeNull()
  })
})
