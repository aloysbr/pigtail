import type { Theme } from '@/api/types'
import { fold } from '@/domain/search'

/** Projects the capture mode proposes at most (t10 capProjects). */
export const MAX_PROJECTS = 5

/** What orders the proposals: the capture's text (a project named in it) and the current project's id. */
export interface ProposeCtx {
  named?: string
  current?: string
}

function escRe(x: string): string {
  return x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** True when the theme's name is a whole word (or words) of the text, accents and case ignored (t10). */
function namedIn(text: string, t: Theme): boolean {
  const name = fold(t.name).trim()
  if (!name || !text) return false
  return new RegExp(`(^|[^a-z0-9])${escRe(name)}($|[^a-z0-9])`).test(fold(text))
}

/** « À qualifier » is out of v1 (spec § Hors périmètre): never a capture's destination. */
function isQualify(t: Theme): boolean {
  return fold(t.name).trim() === 'a qualifier'
}

/**
 * Where a capture may go (t10 capProjects, without « À qualifier »): the projects named in the line, then the current
 * project, then the fixed list — the themes in the order the daemon lists them; every scope (D1), archived ones left out;
 * no duplicate, MAX_PROJECTS at most.
 */
export function proposeProjects(themes: readonly Theme[], ctx: ProposeCtx): Theme[] {
  const out: Theme[] = []
  const seen = new Set<string>()
  const add = (t: Theme | undefined) => {
    if (!t || seen.has(t.id) || t.archived || isQualify(t)) return
    seen.add(t.id)
    out.push(t)
  }
  if (ctx.named) for (const t of themes) if (namedIn(ctx.named, t)) add(t)
  if (ctx.current) add(themes.find((t) => t.id === ctx.current))
  for (const t of themes) add(t)
  return out.slice(0, MAX_PROJECTS)
}

/** Why a project is proposed, as t10 prints it beside the line: named in the line, the current project, or nothing. */
export function whyOf(t: Theme, ctx: ProposeCtx): string {
  if (ctx.named && namedIn(ctx.named, t)) return 'nommé dans la ligne'
  if (ctx.current === t.id) return 'projet courant'
  return ''
}
