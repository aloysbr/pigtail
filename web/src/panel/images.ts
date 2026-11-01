import { shownOutputs } from '@/api/outputs'
import type { Output } from '@/api/types'

/** An image the preview can show: an output with an http(s) URL, its served_url for a local file (the outputs server). */
export interface ImageLink {
  /** The output id. */
  id: string
  url: string
  /** The file name, from the URL's path. */
  name: string
  /** The output's title, else its file name. */
  caption: string
}

const IMG_EXT = /\.(png|jpe?g|gif|webp|svg)$/i

function httpUrl(raw: string | undefined): URL | null {
  if (!raw) return null
  try {
    const u = new URL(raw)
    return u.protocol === 'http:' || u.protocol === 'https:' ? u : null
  } catch {
    return null
  }
}

function fileName(u: URL): string {
  const last = u.pathname.split('/').filter(Boolean).pop() ?? u.host
  try {
    return decodeURIComponent(last)
  } catch {
    return last
  }
}

/** Where an output opens in a browser: its outputs-server address first (a path output), its own URL otherwise. */
export function webUrl(o: Pick<Output, 'served_url' | 'url'>): string | undefined {
  return o.served_url || o.url
}

/** True when the output is an image the preview shows: a capture, or a URL ending in an image extension, at an http(s) URL (served_url first). */
export function isImage(o: Output): boolean {
  const u = httpUrl(webUrl(o))
  return !!u && (o.kind === 'capture' || IMG_EXT.test(u.pathname))
}

/** The images of a subject's shown outputs, in registration order (the order of « Liens »). */
export function imageLinks(outputs: readonly Output[]): ImageLink[] {
  return shownOutputs(outputs)
    .filter(isImage)
    .map((o) => {
      const url = webUrl(o) as string
      const name = fileName(httpUrl(url) as URL)
      return { id: o.id, url, name, caption: o.title || name }
    })
}
