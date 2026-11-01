import { QueryClientProvider } from '@tanstack/react-query'
import { createHashHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ApiProvider, makeClient } from './api/client'
import { makeSend, SendProvider } from './api/mutate'
import { makeQueryClient } from './api/queries'
import { Shell } from './app/Shell'
import { applyChoice, followSystem, readChoice } from './theme/appearance'
import './theme/globals.css'

// boot.js (inline in index.html) already set the theme before the first paint; this is idempotent, then System follows live.
applyChoice(readChoice())
followSystem()

// Hash history: pigtaild serves only `/`, so no SPA fallback can collide with the API routes.
// The list (#/) and a subject's sheet (#/s/<id>) are both drawn by the Shell, which mirrors the hash (state/route.ts).
const rootRoute = createRootRoute({ component: Shell })
const listRoute = createRoute({ getParentRoute: () => rootRoute, path: '/' })
const sheetRoute = createRoute({ getParentRoute: () => rootRoute, path: '/s/$id' })
const router = createRouter({ routeTree: rootRoute.addChildren([listRoute, sheetRoute]), history: createHashHistory() })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

const queryClient = makeQueryClient()
const get = makeClient()
const send = makeSend()

const root = document.getElementById('app')
if (root) {
  createRoot(root).render(
    <StrictMode>
      <ApiProvider value={get}>
        <SendProvider value={send}>
          <QueryClientProvider client={queryClient}>
            <RouterProvider router={router} />
          </QueryClientProvider>
        </SendProvider>
      </ApiProvider>
    </StrictMode>,
  )
}
