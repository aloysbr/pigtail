import { getUi, setUi, subscribeUi, type UiState } from './ui'

/** Routes are hashes (pigtaild serves only `/`): #/ the backlog, #/s/<id> a subject sheet, #/sessions the Sessions view, #/sessions/<session_id> its panel. The UI store stays the source of truth. */
function decode(x: string): string | null {
  try {
    return decodeURIComponent(x)
  } catch {
    return null
  }
}

/** The subject id of a hash route, null when it names none. */
export function parseHash(hash: string): string | null {
  const m = /^#\/s\/([^/?#]+)$/.exec(hash)
  return m ? decode(m[1]) : null
}

/** The hash of an open sheet, or of the list. */
export function hashFor(id: string | null): string {
  return id ? `#/s/${encodeURIComponent(id)}` : '#/'
}

/** A Sessions route: `{id: null}` for the view, `{id}` for its panel on a session; null when the hash is not one. */
export function parseSessionsHash(hash: string): { id: string | null } | null {
  if (hash === '#/sessions' || hash === '#/sessions/') return { id: null }
  const m = /^#\/sessions\/([^/?#]+)$/.exec(hash)
  if (!m) return null
  const id = decode(m[1])
  return id ? { id } : null
}

/** The hash a state is at: the Sessions view (and its panel), else the subject sheet, else the list. */
export function hashOf(s: Pick<UiState, 'view' | 'sessionSel' | 'sessionPanel' | 'panel' | 'sel'>): string {
  if (s.view === 'sessions') return s.sessionPanel && s.sessionSel ? `#/sessions/${encodeURIComponent(s.sessionSel)}` : '#/sessions'
  return hashFor(s.panel && s.sel ? s.sel : null)
}

function same(a: string, b: string): boolean {
  const n = (h: string) => (h === '' || h === '#' ? '#/' : h)
  return n(a) === n(b)
}

/**
 * Reads the hash once (a page loaded on #/s/<id> opens that sheet, on #/sessions the Sessions view), then keeps the two in
 * step: the store writes the address with replaceState (j/k must not stack one history entry per row), a hash change
 * (back, a pasted link) writes the store. Returns the uninstall.
 */
export function installHashSync(): () => void {
  const fromHash = () => {
    const hash = window.location.hash
    if (same(hash, hashOf(getUi()))) return
    const sess = parseSessionsHash(hash)
    if (sess) {
      setUi(sess.id ? { view: 'sessions', sessionSel: sess.id, sessionPanel: true, panel: false } : { view: 'sessions', sessionPanel: false, panel: false })
      return
    }
    const id = parseHash(hash)
    if (id) setUi({ view: 'backlog', sel: id, panel: true, sessionPanel: false })
    else setUi({ view: 'backlog', panel: false, sessionPanel: false })
  }
  fromHash()
  const unsub = subscribeUi(() => {
    const want = hashOf(getUi())
    if (same(window.location.hash, want)) return
    try {
      window.history.replaceState(window.history.state, '', want)
    } catch {
      // a sandboxed frame may refuse history writes: the sheet still works, the address does not follow
    }
  })
  window.addEventListener('hashchange', fromHash)
  return () => {
    unsub()
    window.removeEventListener('hashchange', fromHash)
  }
}
