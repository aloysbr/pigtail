/** One line of « Liens »: its type chip, the target (a link when safe, plain text otherwise), the digit that opens it. */
export interface LinkItem {
  key: string
  chip: string
  text: string
  href: string | null
  title?: string
  /** 1-9 when a digit opens it; a link with no safe href, or past the 9th, has none. */
  digit?: number
  /** Its index among the subject's images when it is one (http(s) only, D15): it opens the preview, not a tab. */
  image?: number
  /** The deliverable group it stands for (its key, D2): a button that opens the reader, not a link. */
  group?: string
  /** The group's count, « 21 tours · t23, hier 15:11 » (`lk-n`). */
  count?: string
}

/** A link when its target is safe, plain text otherwise: an agent-written URL never becomes a script. An image link opens the preview (a click, Space). */
function Target({ it, onImage, onGroup }: { it: LinkItem; onImage?: (index: number) => void; onGroup?: (key: string) => void }) {
  if (it.group !== undefined) {
    const key = it.group
    return (
      <button
        type="button"
        className="lnk-b"
        title={it.title}
        data-group={key}
        aria-keyshortcuts={it.digit ? String(it.digit) : undefined}
        onClick={() => onGroup?.(key)}
      >
        {it.text}
      </button>
    )
  }
  if (!it.href) return <span className="lnk-t" title={it.title}>{it.text}</span>
  const web = it.href.startsWith('http')
  return (
    <a
      className="lnk"
      href={it.href}
      title={it.title}
      target={web ? '_blank' : undefined}
      rel={web ? 'noopener noreferrer' : undefined}
      aria-keyshortcuts={it.digit ? String(it.digit) : undefined}
      data-img={it.image !== undefined ? it.key : undefined}
      onClick={
        it.image !== undefined && onImage
          ? (e) => {
              e.preventDefault()
              onImage(it.image as number)
            }
          : undefined
      }
      onKeyDown={
        it.image !== undefined && onImage
          ? (e) => {
              if (e.key !== ' ' || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return
              e.preventDefault()
              onImage(it.image as number)
            }
          : undefined
      }
    >
      {it.text}
    </a>
  )
}

export function LinkRow({ it, onImage, onGroup }: { it: LinkItem; onImage?: (index: number) => void; onGroup?: (key: string) => void }) {
  return (
    <li className={it.group !== undefined ? 'lk-g' : undefined}>
      <span className="chip">{it.chip}</span>
      <Target it={it} onImage={onImage} onGroup={onGroup} />
      {it.count ? <span className="lk-n">{it.count}</span> : null}
      {it.digit ? (
        <kbd className="lk-k" aria-hidden="true">
          {it.digit}
        </kbd>
      ) : null}
    </li>
  )
}
