import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderApp } from '@/test/render'

describe('Empty states', () => {
  it('a search with no result: « Aucun sujet ne correspond », its sentence, « Effacer la recherche »', async () => {
    const { user } = await renderApp()
    await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'zzz')
    expect(screen.getByRole('heading', { name: 'Aucun sujet ne correspond' })).toBeInTheDocument()
    expect(screen.getByText('Aucun titre ne contient « zzz » dans le backlog.')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Effacer tous les filtres' }).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: 'Effacer la recherche' }))
    expect(screen.queryByRole('heading', { name: 'Aucun sujet ne correspond' })).toBeNull()
    expect(screen.getByText('18 sujets')).toBeInTheDocument()
  })

  it('an empty area: « Aucun sujet dans l’aire X »', async () => {
    const { user } = await renderApp()
    await user.click(screen.getByRole('button', { name: /^Vide/ }))
    expect(screen.getByRole('heading', { name: "Aucun sujet dans l'aire Vide" })).toBeInTheDocument()
    expect(document.querySelector('.empty6 .empty-ic svg path')?.getAttribute('d')).toBe(document.querySelector('.fsearch svg path')?.getAttribute('d'))
    expect(screen.getByText("Cette aire est vide pour l'instant ; ses sujets apparaîtront ici.")).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Effacer le filtre' }))
    expect(screen.getByText('18 sujets')).toBeInTheDocument()
  })

  it('the clear buttons are solid buttons (not the dashed add style); « Effacer la recherche » carries no key hint (Esc does not clear it)', async () => {
    const { user } = await renderApp()
    await user.type(screen.getByRole('searchbox', { name: 'Rechercher dans le backlog' }), 'zzz')
    const q = screen.getByRole('button', { name: 'Effacer la recherche' })
    const all = screen.getAllByRole('button', { name: /^Effacer tous les filtres/ }).find((b) => b.closest('.empty6'))
    for (const b of [q, all as HTMLElement]) {
      expect(b).toHaveClass('btnl')
      expect(b).not.toHaveClass('fadd')
    }
    expect(q.querySelector('kbd')).toBeNull()
    expect(all?.querySelector('kbd')).toBeNull()
  })

  it('shows skeleton rows while the first read is pending', async () => {
    const { container } = await renderAppPending()
    expect(container.querySelectorAll('.skel').length).toBeGreaterThan(0)
    expect(screen.queryByRole('region', { name: 'Sujets' })).toBeNull()
  })
})

async function renderAppPending() {
  const { vi } = await import('vitest')
  const { render } = await import('@testing-library/react')
  const { QueryClientProvider } = await import('@tanstack/react-query')
  const { ApiProvider } = await import('@/api/client')
  const { makeQueryClient } = await import('@/api/queries')
  const { Shell } = await import('@/app/Shell')
  const get = vi.fn(() => new Promise<never>(() => undefined)) as unknown as Parameters<typeof ApiProvider>[0]['value']
  return render(
    <ApiProvider value={get}>
      <QueryClientProvider client={makeQueryClient()}>
        <Shell />
      </QueryClientProvider>
    </ApiProvider>,
  )
}
