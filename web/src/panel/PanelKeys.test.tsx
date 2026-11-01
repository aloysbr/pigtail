import { fireEvent, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { getUi } from '@/state/ui'
import { groupButton, renderApp } from '@/test/render'
import { hashFor, parseHash } from '@/state/route'

function key(k: string, code = '') {
  fireEvent.keyDown(document.activeElement ?? document.body, { key: k, code })
}

function panelTitle(): string | null {
  return document.querySelector('.lpanel h2')?.textContent ?? null
}

afterEach(() => {
  window.history.replaceState(null, '', '/')
})

describe('Panel keys', () => {
  it('⏎ opens the selected subject; j/k make the sheet follow; Esc closes it', async () => {
    await renderApp()
    groupButton('th:th_pig').focus()
    key('ArrowRight')
    key('j')
    expect(document.querySelector('.lpanel')).toBeNull()
    key('Enter')
    expect(panelTitle()).toBe('Session relay')
    key('j')
    expect(panelTitle()).toBe('Atlas web')
    key('k')
    expect(panelTitle()).toBe('Session relay')
    key('Escape')
    expect(document.querySelector('.lpanel')).toBeNull()
    // the selection stays and the row gets the focus back
    expect(getUi().sel).toBe('pig-live')
    expect(document.activeElement).toBe(document.querySelector('li[data-id="pig-live"]'))
  })

  it('⏎ on a group header still folds it (the button acts), it opens no sheet', async () => {
    await renderApp()
    key('j')
    groupButton('sp:perso/Allotment').focus()
    key('Enter')
    expect(document.querySelector('.lpanel')).toBeNull()
  })

  it('⏎ with nothing selected opens nothing', async () => {
    await renderApp()
    key('Enter')
    expect(document.querySelector('.lpanel')).toBeNull()
  })
})

describe('Panel route (#/s/<id>)', () => {
  it('parses and builds the hash route', () => {
    expect(parseHash('#/s/sub_abc')).toBe('sub_abc')
    expect(parseHash('#/s/a%2Fb')).toBe('a/b')
    expect(parseHash('#/')).toBeNull()
    expect(parseHash('')).toBeNull()
    expect(parseHash('#/s/')).toBeNull()
    expect(hashFor('sub_abc')).toBe('#/s/sub_abc')
    expect(hashFor(null)).toBe('#/')
  })

  it('opening a sheet writes #/s/<id>; closing it goes back to #/', async () => {
    const { user } = await renderApp()
    await user.click(groupButton('th:th_pig'))
    await user.click(document.querySelector('li[data-id="pig-r2"]') as HTMLElement)
    expect(window.location.hash).toBe('#/s/pig-r2')
    key('Escape')
    expect(window.location.hash).toBe('#/')
  })

  it('a page loaded on #/s/<id> opens that sheet', async () => {
    window.history.replaceState(null, '', '/#/s/watering-1')
    await renderApp()
    expect(await screen.findByRole('heading', { level: 2, name: 'Watering atlas' })).toBeInTheDocument()
    expect(getUi().sel).toBe('watering-1')
  })

  it('a hash change (back, a pasted link) follows', async () => {
    await renderApp()
    window.history.replaceState(null, '', '/#/s/meth-1')
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    expect(await screen.findByRole('heading', { level: 2, name: 'Règle des PR' })).toBeInTheDocument()
  })
})
