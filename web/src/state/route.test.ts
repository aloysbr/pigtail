import { afterEach, describe, expect, it } from 'vitest'
import { hashOf, installHashSync, parseHash, parseSessionsHash } from './route'
import { getUi, openBacklog, openPanel, openSessions, setUi } from './ui'

let uninstall: (() => void) | null = null
afterEach(() => {
  uninstall?.()
  uninstall = null
})

describe('Sessions routes', () => {
  it('parses #/sessions and #/sessions/<id>, and nothing else', () => {
    expect(parseSessionsHash('#/sessions')).toEqual({ id: null })
    expect(parseSessionsHash('#/sessions/s%2Fa')).toEqual({ id: 's/a' })
    expect(parseSessionsHash('#/s/sub_1')).toBeNull()
    expect(parseSessionsHash('#/')).toBeNull()
    expect(parseSessionsHash('#/sessions/%E0%A4%A')).toBeNull()
    expect(parseHash('#/sessions')).toBeNull()
  })

  it('hashOf: the view, its panel, else the backlog routes', () => {
    const base = { sel: null, panel: false, sessionSel: null, sessionPanel: false } as const
    expect(hashOf({ ...base, view: 'sessions' })).toBe('#/sessions')
    expect(hashOf({ ...base, view: 'sessions', sessionSel: 's1', sessionPanel: false })).toBe('#/sessions')
    expect(hashOf({ ...base, view: 'sessions', sessionSel: 's1', sessionPanel: true })).toBe('#/sessions/s1')
    expect(hashOf({ ...base, view: 'backlog', sel: 'sub_1', panel: true })).toBe('#/s/sub_1')
    expect(hashOf({ ...base, view: 'backlog' })).toBe('#/')
  })

  it('a page loaded on #/sessions opens the view; on #/sessions/<id> the session panel', () => {
    window.history.replaceState(null, '', '/#/sessions')
    uninstall = installHashSync()
    expect(getUi().view).toBe('sessions')
    expect(getUi().sessionPanel).toBe(false)
    uninstall()
    window.history.replaceState(null, '', '/#/sessions/s-idle-arb')
    uninstall = installHashSync()
    expect(getUi()).toMatchObject({ view: 'sessions', sessionSel: 's-idle-arb', sessionPanel: true, panel: false })
  })

  it('opening the view writes #/sessions; Backlog brings #/ back; a subject sheet writes #/s/<id> and leaves the view', () => {
    uninstall = installHashSync()
    openSessions()
    expect(window.location.hash).toBe('#/sessions')
    openBacklog()
    expect(getUi().view).toBe('backlog')
    expect(window.location.hash).toBe('#/')
    openSessions()
    openPanel('sub_ds')
    expect(getUi().view).toBe('backlog')
    expect(window.location.hash).toBe('#/s/sub_ds')
  })

  it('a hash change (back, a pasted link) moves the store between the two views', () => {
    uninstall = installHashSync()
    window.location.hash = '#/sessions/s-busy-q'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    expect(getUi()).toMatchObject({ view: 'sessions', sessionSel: 's-busy-q', sessionPanel: true })
    window.location.hash = '#/'
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    expect(getUi()).toMatchObject({ view: 'backlog', sessionPanel: false, panel: false })
    setUi({ view: 'sessions' })
    expect(window.location.hash).toBe('#/sessions')
  })
})
