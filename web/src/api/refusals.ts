import type { ApiError } from './client'

/** What a refusal toast names: the subject's short title, its scope, the cell when known, and the action refused. */
export interface RefusalCtx {
  title: string
  scope?: string
  cell?: string
  action?: 'move' | 'undo' | 'launch' | 'write' | 'capture'
}

/** Copies that are not refusals: rank toasts (D23, D9-10), undo (D9-9, D9-10), empty project list (D21). */
export const COPY = {
  rankMoved: (x: string) => `Rang de « ${x} » modifié`,
  rankRestored: (x: string) => `Rang de « ${x} » rétabli`,
  undone: (label: string) => `Annulé · ${label}`,
  undoStale: (x: string) => `Impossible d'annuler : « ${x} » a changé depuis.`,
  noProjects: 'Aucun projet à proposer : crée-en un depuis la CLI.',
} as const

const MAINTENANCE = 'Maintenance en cours : réessaie quand elle est finie.'
const BURST = "Trop de lancements d'affilée : réessaie dans un instant."
const CMUX = "cmux ne répond pas : la session n'a pas été ouverte."

function detail(err: ApiError, key: string): string | undefined {
  const d = err.details
  if (d && typeof d === 'object' && key in d) {
    const v = (d as Record<string, unknown>)[key]
    if (typeof v === 'string' && v) return v
  }
  return undefined
}

/**
 * The toast of a refused write: plan table D21 (copies D9 of the spec reused word for word).
 * Dispatch by the error body's code first, then by status; never the daemon's raw (English) message.
 */
export function refusalToast(err: ApiError, ctx: RefusalCtx): string {
  const x = ctx.title
  const scope = ctx.scope || detail(err, 'scope') || ''
  const cell = ctx.cell || detail(err, 'cell')
  // without a cell name: « la cellule du scope X » (D9-18)
  const cellOf = cell ? `cellule ${cell}` : `la cellule du scope ${scope}`
  const launching = ctx.action === 'launch'
  const forbidden = `Pigtail n'a pas le droit de faire ce changement sur « ${x} ».`
  const unavailable = `Le lancement de « ${x} » est indisponible pour l'instant : réessaie dans un moment.`

  if (err.status === 0) return `pigtaild ne répond pas : l'action sur « ${x} » n'a pas été faite.`
  if (err.unreadable && err.status !== 403) return `Réponse illisible de pigtaild : l'action sur « ${x} » n'a pas été faite.`

  switch (err.code) {
    case 'SESSION_LIVE':
      return `Une session est déjà ouverte pour « ${x} » (${cellOf}).`
    case 'LAUNCH_IN_PROGRESS':
      return `Un lancement est déjà en cours pour « ${x} ».`
    case 'LAUNCH_BURST':
      return BURST
    case 'NO_CELL':
      return `Aucune cellule configurée pour le scope ${scope} : ouvre la session à la main.`
    case 'NO_LAUNCH_DIR':
      return `Pas de dossier de lancement pour le scope ${scope}.`
    case 'BYPASS_REFUSED':
      return `${cell ? `La cellule ${cell}` : `La cellule du scope ${scope}`} contourne les permissions : lancement refusé.`
    case 'LAUNCH_OVER_MARGIN':
      return 'Marge du compte dépassée : lancement reporté.'
    case 'MAINTENANCE_FROZEN':
      return MAINTENANCE
    case 'RANK_CHANGED':
      return ctx.action === 'undo' ? COPY.undoStale(x) : `Le rang de « ${x} » a changé entre-temps : rien n'a été modifié.`
    case 'FORBIDDEN_FOR_WEB':
    case 'SCOPE_MISMATCH':
      return forbidden
    case 'CMUX_CREATE_FAILED':
    case 'CMUX_UNREACHABLE':
      return CMUX
    case 'REGISTRY_UNREADABLE':
    case 'LIVE_UNKNOWN':
    case 'CELL_MODE_UNREADABLE':
      return unavailable
    default:
  }

  switch (err.status) {
    case 400:
      return `Demande refusée par pigtaild : l'action sur « ${x} » n'a pas été faite.`
    case 401:
      return "Session expirée : l'action n'a pas été faite. Recharge la page."
    case 403:
      return forbidden
    case 404:
      return `« ${x} » n'existe plus : la liste se met à jour.`
    case 409:
      return `L'action sur « ${x} » est en conflit avec l'état actuel : rien n'a été modifié.`
    case 423:
      return MAINTENANCE
    case 429:
      return launching ? BURST : "Trop de demandes d'affilée : réessaie dans un instant."
    case 502:
      return `Réponse inattendue de pigtaild : l'action sur « ${x} » n'a pas été faite.`
    case 503:
      return launching ? unavailable : `pigtaild est indisponible pour l'instant : l'action sur « ${x} » n'a pas été faite, réessaie dans un moment.`
    default:
      return `Erreur de pigtaild : l'action sur « ${x} » n'a pas été faite.`
  }
}

/** Why an answer to a wait did not go, in words (spec § Copy: what happened); never the daemon's raw (English) message. */
function answerCause(err: ApiError): string {
  if (err.status === 0) return 'pigtaild ne répond pas'
  if (err.unreadable && err.status !== 403) return 'la réponse de pigtaild est illisible'
  if (err.code === 'MAINTENANCE_FROZEN' || err.status === 423) return 'une maintenance est en cours, réessaie quand elle est finie'
  switch (err.status) {
    case 400:
      return /secret/i.test(err.message) ? "elle ressemble à un secret (n'en envoie jamais)" : "pigtaild n'accepte pas cette forme de réponse pour cette attente"
    case 401:
      return 'la page a expiré, recharge-la'
    case 403:
      return 'cette attente se répond dans le terminal de la session'
    case 404:
      return "cette attente n'existe plus"
    case 409:
      return "l'attente a changé entre-temps (déjà répondue ou fermée)"
    case 429:
      return "trop de demandes d'affilée, réessaie dans un instant"
    case 503:
      return "pigtaild est indisponible pour l'instant, réessaie dans un moment"
    default:
      return 'pigtaild a rencontré une erreur'
  }
}

/** The line under the field after a failed answer (T12, G5): what happened, and that nothing is lost. */
export function answerFailed(err: ApiError): string {
  return `Ta réponse n'est pas partie : ${answerCause(err)}. Elle est restée dans le champ.`
}
