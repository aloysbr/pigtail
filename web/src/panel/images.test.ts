import { describe, expect, it } from 'vitest'
import type { Output } from '@/api/types'
import { imageLinks } from './images'

let seq = 0
function out(p: Partial<Output> & { kind: string }): Output {
  seq += 1
  return { seq, id: `out_${seq}`, subject_id: 's', key: `k${seq}`, title: '', ts: 0, ...p }
}

describe('imageLinks: the outputs the preview shows (http(s) images only, D15)', () => {
  it('a capture with an http(s) URL is an image, whatever its extension', () => {
    const got = imageLinks([out({ kind: 'capture', url: 'https://x.example/shots/view', title: 'Vue' })])
    expect(got.map((i) => [i.url, i.name, i.caption])).toEqual([['https://x.example/shots/view', 'view', 'Vue']])
  })

  it('another kind is an image by its extension (png, jpg, jpeg, gif, webp, svg; any case, a query string allowed)', () => {
    const urls = ['a.png', 'b.JPG', 'c.jpeg', 'd.gif', 'e.webp', 'f.svg', 'g.png?v=2'].map((f) => `https://x.example/${f}`)
    const got = imageLinks([...urls.map((url) => out({ kind: 'link', url })), out({ kind: 'link', url: 'https://x.example/page.html' }), out({ kind: 'pr', url: 'https://x.example/pull/1' })])
    expect(got.map((i) => i.name)).toEqual(['a.png', 'b.JPG', 'c.jpeg', 'd.gif', 'e.webp', 'f.svg', 'g.png'])
  })

  it('a local image (path, no url) is excluded: no route serves a local file (daemon-side work)', () => {
    expect(imageLinks([out({ kind: 'capture', path: '/Users/x/_outputs/a.png' })])).toEqual([])
  })

  it('a non-http URL is excluded (file:, data:, obsidian:)', () => {
    const got = imageLinks([
      out({ kind: 'capture', url: 'file:///Users/x/a.png' }),
      out({ kind: 'capture', url: 'data:image/png;base64,AAAA' }),
      out({ kind: 'capture', url: 'obsidian://open?path=a.png' }),
    ])
    expect(got).toEqual([])
  })

  it('a purged output is excluded; the order is the registration order', () => {
    const a = out({ kind: 'capture', url: 'https://x.example/a.png' })
    const b = out({ kind: 'capture', url: 'https://x.example/b.png', purged_at: 1 })
    const c = out({ kind: 'capture', url: 'https://x.example/c.png' })
    expect(imageLinks([c, b, a]).map((i) => i.id)).toEqual([a.id, c.id])
  })
})

describe('imageLinks: served_url (the outputs server, sub_x000000000000025)', () => {
  it('a capture with a path and a served_url is an image at its served_url', () => {
    const served = 'http://127.0.0.1:7801/Users/x/_outputs/a.png'
    const got = imageLinks([out({ kind: 'capture', path: '/Users/x/_outputs/a.png', served_url: served })])
    expect(got.map((i) => [i.url, i.name])).toEqual([[served, 'a.png']])
  })

  it('served_url wins over url', () => {
    const served = 'http://127.0.0.1:7801/Users/x/b.png'
    expect(imageLinks([out({ kind: 'capture', url: 'https://x.example/b.png', served_url: served })]).map((i) => i.url)).toEqual([served])
  })
})
