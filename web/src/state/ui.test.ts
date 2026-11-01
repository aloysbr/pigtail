import { beforeEach, describe, expect, it } from 'vitest'
import { closeLayer, getUi, openReader, resetUi, setUi, toggleScope } from './ui'

describe('toggleScope', () => {
  beforeEach(() => setUi({ scopesOff: [] }))

  it('a scope switched off from a wider list survives a toggle from a narrower one (Sessions view, then backlog pills)', () => {
    toggleScope('lab', ['perso', 'globex', 'lab'])
    expect(getUi().scopesOff).toEqual(['lab'])
    toggleScope('globex', ['perso', 'globex'])
    expect(getUi().scopesOff).toEqual(['lab', 'globex'])
    toggleScope('globex', ['perso', 'globex'])
    expect(getUi().scopesOff).toEqual(['lab'])
  })

  it('never switches the last scope of the calling list off', () => {
    setUi({ scopesOff: ['lab'] })
    toggleScope('globex', ['globex'])
    expect(getUi().scopesOff).toEqual(['lab'])
  })
})

describe('openReader', () => {
  beforeEach(() => resetUi())

  it('sets ui.reader on the latest version by default', () => {
    expect(getUi().reader).toBeNull()
    openReader('sub_1', '/x/mockup')
    expect(getUi().reader).toEqual({ subjectId: 'sub_1', groupKey: '/x/mockup', tour: null })
  })

  it('opens on a given round', () => {
    openReader('sub_1', '/x/mockup', 19)
    expect(getUi().reader).toEqual({ subjectId: 'sub_1', groupKey: '/x/mockup', tour: 19 })
  })
})

describe('closeLayer: preview > reader > palette', () => {
  beforeEach(() => resetUi())

  it('peels the preview, then the reader, then the palette, then the sheet', () => {
    setUi({ panel: true, sel: 'sub_1', palette: true, reader: { subjectId: 'sub_1', groupKey: '/x', tour: 3 }, preview: { subjectId: 'sub_1', index: 0 } })
    expect(closeLayer()).toBe(true)
    expect(getUi().preview).toBeNull()
    expect(getUi().reader).not.toBeNull()
    expect(closeLayer()).toBe(true)
    expect(getUi().reader).toBeNull()
    expect(getUi().palette).toBe(true)
    expect(closeLayer()).toBe(true)
    expect(getUi().palette).toBe(false)
    expect(getUi().panel).toBe(true)
  })

  it('the next opening starts from the latest version', () => {
    openReader('sub_1', '/x', 19)
    closeLayer()
    openReader('sub_1', '/x')
    expect(getUi().reader?.tour).toBeNull()
  })
})
