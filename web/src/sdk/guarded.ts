import { useRef, useState } from 'react'
import { ApiError } from '@/api/client'
import { refusalToast } from '@/api/refusals'
import { pushToast } from '@/ui/toasts'

/**
 * Runs one write of the block: `busy` while it is in flight (a second press does nothing), and a refusal goes through the
 * existing refusal toast (naming the subject), never a silent failure. Resolves true when the write went through.
 */
export function useGuarded(title: string, scope?: string) {
  const [busy, setBusy] = useState(false)
  const flying = useRef(false)
  const run = async (write: () => Promise<unknown>, action: 'write' | 'launch' = 'write'): Promise<boolean> => {
    if (flying.current) return false
    flying.current = true
    setBusy(true)
    try {
      await write()
      return true
    } catch (e) {
      const err = e instanceof ApiError ? e : new ApiError(0, String(e))
      pushToast({ text: refusalToast(err, { title, scope, action }) })
      return false
    } finally {
      flying.current = false
      setBusy(false)
    }
  }
  return { busy, run }
}
