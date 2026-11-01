import { cn } from '@/lib/utils'

/** The dot's group: waiting (the accent and its halo), working (filled grey), ended (the grey ring). */
export type DotGroup = 'wait' | 'work' | 'idle'

export function Dot({ g }: { g: DotGroup }) {
  return (
    <span className={cn('sdot-w', `sg-${g}`, g === 'wait' && 'sess-wait')} aria-hidden="true">
      <span className="dot" />
    </span>
  )
}
