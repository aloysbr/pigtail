import { describe, expect, it } from 'vitest'
import { getUi } from '@/state/ui'
import { renderApp } from '@/test/render'

describe('a #/s/<id> that names no subject', () => {
  it('once the data is read, the sheet stays closed and the address goes back to #/', async () => {
    window.location.hash = '#/s/gone'
    await renderApp()
    expect(getUi().panel).toBe(false)
    expect(window.location.hash).toBe('#/')
  })

  it('a known subject keeps its hash and its sheet', async () => {
    window.location.hash = '#/s/pig-r1'
    await renderApp()
    expect(getUi().panel).toBe(true)
    expect(window.location.hash).toBe('#/s/pig-r1')
  })

  it('a failed re-read does not clear the hash', async () => {
    window.location.hash = '#/s/pig-r1'
    const r = await renderApp()
    r.setFailing(true)
    await r.refetch()
    expect(window.location.hash).toBe('#/s/pig-r1')
    expect(getUi().panel).toBe(true)
  })
})
