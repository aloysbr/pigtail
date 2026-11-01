import { useSyncExternalStore } from 'react'

/** Under this width the keys are hidden and the layout is the phone's (t10 `.mobile`). */
export const PHONE_MAX = 640

function subscribe(l: () => void) {
  window.addEventListener('resize', l)
  return () => window.removeEventListener('resize', l)
}

/** True on a phone-width window (read by a component deep in the list, without threading the shell's width down). */
export function usePhone(): boolean {
  return useSyncExternalStore(subscribe, () => window.innerWidth < PHONE_MAX, () => false)
}
