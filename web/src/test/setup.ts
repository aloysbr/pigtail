import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'
import { resetUi } from '@/state/ui'
import { resetWidths } from '@/state/widths'

afterEach(() => {
  cleanup()
  try {
    localStorage.clear()
    sessionStorage.clear()
  } catch {
    // storage may be unavailable: nothing to clear
  }
  // the sheet's route lives in the hash: a test that opened a sheet must not open it in the next one
  if (typeof window !== 'undefined') window.history.replaceState(null, '', '/')
  resetUi()
  // after the storage is cleared: the widths re-read an empty storage
  resetWidths()
})

// cmdk scrolls its selected item into view; jsdom has no layout, so no scrollIntoView
if (typeof Element !== 'undefined' && !Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = function scrollIntoView() {}

// cmdk measures its list with a ResizeObserver; jsdom has none and computes no layout anyway
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}
