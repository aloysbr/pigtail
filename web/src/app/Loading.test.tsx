import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ROUTES } from '@/api/queries'
import { renderApp } from '@/test/render'

/** Goût G2: the loading state is the loaded frame, skeleton rows in place of the list (no layout jump). */
describe('Loading state keeps the frame', () => {
  it('at 1440: sidebar column, header, skeleton rows and the footer all there before the data arrives', async () => {
    await renderApp(undefined, { wait: false })
    const app = document.querySelector('.app') as HTMLElement
    expect(app).not.toHaveClass('nodock')
    const side = screen.getByRole('complementary', { name: 'Navigation' })
    expect(side.parentElement).toBe(app)
    expect(side.nextElementSibling).toBe(document.querySelector('main.lmain'))
    const s = within(side)
    expect(s.getByText('Pigtail', { selector: '.brand b' })).toBeInTheDocument()
    expect(s.getByRole('button', { name: /Palette/ })).toBeInTheDocument()
    expect(s.getByRole('button', { name: /Ranger/ })).toBeInTheDocument()
    expect(s.getByRole('button', { name: /Backlog/ })).toBeInTheDocument()
    const head = document.querySelector('header.d-head') as HTMLElement
    expect(head).not.toBeNull()
    expect(within(head).getByRole('heading', { name: 'Backlog' })).toBeInTheDocument()
    const busy = document.querySelector('[aria-busy="true"]') as HTMLElement
    expect(busy.querySelectorAll('.skel').length).toBeGreaterThan(0)
    expect(head.compareDocumentPosition(busy) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const main = document.querySelector('main.lmain') as HTMLElement
    expect(main.contains(screen.getByRole('contentinfo', { name: 'Raccourcis clavier' }))).toBe(true)
  })

  it('at 1024: no sidebar, full-width main, header with the menu button', async () => {
    await renderApp(undefined, { wait: false, width: 1024 })
    expect(screen.queryByRole('complementary', { name: 'Navigation' })).toBeNull()
    const head = document.querySelector('header.d-head') as HTMLElement
    expect(within(head).getByRole('button', { name: 'Navigation' })).toBeInTheDocument()
    expect(within(head).getByRole('heading', { name: 'Backlog' })).toBeInTheDocument()
    expect(document.querySelectorAll('.skel').length).toBeGreaterThan(0)
  })
})

/** GET /peers only marks live sessions and can take minutes on a loaded Mac: the list never waits for it. */
describe('Peers do not hold the list', () => {
  it('renders the rows while /peers is still pending (no skeleton)', async () => {
    await renderApp(undefined, { hang: [ROUTES.peers] })
    expect(screen.getByRole('region', { name: 'Sujets' })).toBeInTheDocument()
    expect(document.querySelectorAll('.skel').length).toBe(0)
    expect(document.querySelectorAll('button[data-g]').length).toBeGreaterThan(0)
  })
})
