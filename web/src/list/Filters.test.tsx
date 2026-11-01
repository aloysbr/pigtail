import { fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { makeBacklog } from '@/test/backlog'
import { renderApp, rowIds, unfoldAll } from '@/test/render'

function menu() {
  return within(screen.getByRole('menu', { name: 'Filtrer' }))
}

describe('Filters and search', () => {
  it('« atlas » + Statut Ouvert leaves the open subjects whose title has « atlas »; clearing all gives everything back', async () => {
    const { user } = await renderApp()
    // One change event, not a keystroke each: re-rendering the whole backlog per
    // letter took most of the 5 s budget on a loaded machine.
    fireEvent.change(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), { target: { value: 'atlas' } })
    expect(await screen.findByText('5 sujets')).toBeInTheDocument()
    expect(screen.getByText('Recherche').closest('.fchip')).toHaveTextContent('« atlas »')

    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    await user.click(menu().getByRole('menuitemradio', { name: /Ouvert/ }))
    unfoldAll()
    expect(rowIds().sort()).toEqual(['pig-r1', 'pig-r2', 'watering-1', 'wk-2'])
    expect(screen.getByText('4 sujets')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Effacer tous les filtres' }))
    expect(screen.getByText('18 sujets')).toBeInTheDocument()
    expect(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' })).toHaveValue('')
    expect(document.querySelector('.fchip')).toBeNull()
  })

  it('removes a single chip', async () => {
    const { user } = await renderApp()
    await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'atlas')
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    await user.click(menu().getByRole('menuitemradio', { name: /Ouvert/ }))
    await user.click(screen.getByRole('button', { name: 'Retirer le filtre Statut' }))
    expect(screen.getByText('5 sujets')).toBeInTheDocument()
    expect(screen.getByText('Recherche').closest('.fchip')).toBeInTheDocument()
  })

  it('picks an area by its digit, read from the key code (AZERTY « é » on Digit2)', async () => {
    const { user } = await renderApp()
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    expect(menu().getAllByRole('menuitemradio').map((b) => b.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining('Allotment'), expect.stringContaining('Garden')]),
    )
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'é', code: 'Digit2' })
    expect(screen.queryByRole('menu', { name: 'Filtrer' })).toBeNull()
    expect(screen.getByText('Aire').closest('.fchip')).toHaveTextContent('Garden')
    expect(screen.getByText('2 sujets')).toBeInTheDocument()
  })
})

describe('Filter menu area order', () => {
  it('D-order: areas follow the sidebar (scope by scope), so digits 1-9 match what the sidebar shows', async () => {
    const data = makeBacklog()
    data.spaces = [
      { name: 'Allotment', scope: 'perso' },
      { name: 'Vineyard', scope: 'globex' },
      { name: 'Garden', scope: 'perso' },
    ]
    const { user } = await renderApp(data)
    const side = [...document.querySelectorAll('.side .nav-i:not(.nav-proj) .nav-l')].map((e) => e.textContent).filter((t) => t !== 'Backlog' && t !== 'Sessions')
    await user.click(screen.getByRole('button', { name: /Filtre/ }))
    const items = within(screen.getByRole('menu', { name: 'Filtrer' }))
      .getAllByRole('menuitemradio')
      .filter((b) => b.querySelector('.gm-n'))
      .map((b) => b.querySelector('span')?.firstChild?.textContent)
    expect(items).toEqual(side)
  })
})
