import { describe, expect, it } from 'vitest'
import { ApiError } from './client'
import { COPY, refusalToast } from './refusals'

const X = 'Atlas web'
const err = (status: number, code?: string, details?: unknown) => new ApiError(status, 'english daemon message', code, details)
const launch = { title: X, scope: 'perso', cell: 'mp', action: 'launch' as const }

describe('refusalToast: D21 table, by code first', () => {
  it('SESSION_LIVE (409)', () => {
    expect(refusalToast(err(409, 'SESSION_LIVE'), launch)).toBe('Une session est déjà ouverte pour « Atlas web » (cellule mp).')
  })
  it('LAUNCH_IN_PROGRESS (409)', () => {
    expect(refusalToast(err(409, 'LAUNCH_IN_PROGRESS'), launch)).toBe('Un lancement est déjà en cours pour « Atlas web ».')
  })
  it('LAUNCH_BURST (429)', () => {
    expect(refusalToast(err(429, 'LAUNCH_BURST'), launch)).toBe("Trop de lancements d'affilée : réessaie dans un instant.")
  })
  it('NO_CELL (409)', () => {
    expect(refusalToast(err(409, 'NO_CELL'), launch)).toBe('Aucune cellule configurée pour le scope perso : ouvre la session à la main.')
  })
  it('NO_LAUNCH_DIR (409)', () => {
    expect(refusalToast(err(409, 'NO_LAUNCH_DIR'), launch)).toBe('Pas de dossier de lancement pour le scope perso.')
  })
  it('BYPASS_REFUSED (403)', () => {
    expect(refusalToast(err(403, 'BYPASS_REFUSED'), launch)).toBe('La cellule mp contourne les permissions : lancement refusé.')
  })
  it('LAUNCH_OVER_MARGIN (409) wins over the 409 default', () => {
    expect(refusalToast(err(409, 'LAUNCH_OVER_MARGIN'), launch)).toBe('Marge du compte dépassée : lancement reporté.')
  })
  it('MAINTENANCE_FROZEN (423)', () => {
    expect(refusalToast(err(423, 'MAINTENANCE_FROZEN'), { title: X, action: 'move' })).toBe('Maintenance en cours : réessaie quand elle est finie.')
  })
  it('RANK_CHANGED (409) on a move', () => {
    expect(refusalToast(err(409, 'RANK_CHANGED'), { title: X, action: 'move' })).toBe("Le rang de « Atlas web » a changé entre-temps : rien n'a été modifié.")
  })
  it('RANK_CHANGED (409) on an undo', () => {
    expect(refusalToast(err(409, 'RANK_CHANGED'), { title: X, action: 'undo' })).toBe("Impossible d'annuler : « Atlas web » a changé depuis.")
  })
  it('404, subject gone', () => {
    expect(refusalToast(err(404, 'NOT_FOUND'), { title: X, action: 'write' })).toBe("« Atlas web » n'existe plus : la liste se met à jour.")
  })
  it('403 FORBIDDEN_FOR_WEB', () => {
    expect(refusalToast(err(403, 'FORBIDDEN_FOR_WEB'), { title: X, action: 'write' })).toBe("Pigtail n'a pas le droit de faire ce changement sur « Atlas web ».")
  })
  it('403 SCOPE_MISMATCH', () => {
    expect(refusalToast(err(403, 'SCOPE_MISMATCH'), { title: X, action: 'move' })).toBe("Pigtail n'a pas le droit de faire ce changement sur « Atlas web ».")
  })
  it('502 CMUX_CREATE_FAILED', () => {
    expect(refusalToast(err(502, 'CMUX_CREATE_FAILED'), launch)).toBe("cmux ne répond pas : la session n'a pas été ouverte.")
  })
  it('503 CMUX_UNREACHABLE', () => {
    expect(refusalToast(err(503, 'CMUX_UNREACHABLE'), launch)).toBe("cmux ne répond pas : la session n'a pas été ouverte.")
  })
  it.each(['REGISTRY_UNREADABLE', 'LIVE_UNKNOWN', 'CELL_MODE_UNREADABLE'])('503 %s', (code) => {
    expect(refusalToast(err(503, code), launch)).toBe("Le lancement de « Atlas web » est indisponible pour l'instant : réessaie dans un moment.")
  })
})

describe('refusalToast: defaults by status (no known code)', () => {
  const w = { title: X, action: 'write' as const }
  it('400, never the daemon’s raw message', () => {
    const t = refusalToast(err(400, 'INVALID_REQUEST'), w)
    expect(t).toBe("Demande refusée par pigtaild : l'action sur « Atlas web » n'a pas été faite.")
    expect(t).not.toContain('english')
  })
  it('401 on a write', () => {
    expect(refusalToast(err(401, 'UNAUTHORIZED'), w)).toBe("Session expirée : l'action n'a pas été faite. Recharge la page.")
  })
  it('403 UNAUTHORIZED (a refused Origin)', () => {
    expect(refusalToast(err(403, 'UNAUTHORIZED'), w)).toBe("Pigtail n'a pas le droit de faire ce changement sur « Atlas web ».")
  })
  it('403 plain text (forbidden-plain) keeps the 403 line', () => {
    expect(refusalToast(new ApiError(403, 'x', undefined, undefined, true), w)).toBe("Pigtail n'a pas le droit de faire ce changement sur « Atlas web ».")
  })
  it('404', () => {
    expect(refusalToast(err(404), w)).toBe("« Atlas web » n'existe plus : la liste se met à jour.")
  })
  it('409', () => {
    expect(refusalToast(err(409, 'CONFLICT'), w)).toBe("L'action sur « Atlas web » est en conflit avec l'état actuel : rien n'a été modifié.")
  })
  it('423', () => {
    expect(refusalToast(err(423), w)).toBe('Maintenance en cours : réessaie quand elle est finie.')
  })
  it('429 on a launch', () => {
    expect(refusalToast(err(429), launch)).toBe("Trop de lancements d'affilée : réessaie dans un instant.")
  })
  it('429 elsewhere', () => {
    expect(refusalToast(err(429), w)).toBe("Trop de demandes d'affilée : réessaie dans un instant.")
  })
  it('500', () => {
    expect(refusalToast(err(500, 'INTERNAL'), w)).toBe("Erreur de pigtaild : l'action sur « Atlas web » n'a pas été faite.")
  })
  it('502', () => {
    expect(refusalToast(err(502), w)).toBe("Réponse inattendue de pigtaild : l'action sur « Atlas web » n'a pas été faite.")
  })
  it('503 on a launch (the launcher off: INTERNAL)', () => {
    expect(refusalToast(err(503, 'INTERNAL'), launch)).toBe("Le lancement de « Atlas web » est indisponible pour l'instant : réessaie dans un moment.")
  })
  it('503 elsewhere', () => {
    expect(refusalToast(err(503, 'INTERNAL'), w)).toBe("pigtaild est indisponible pour l'instant : l'action sur « Atlas web » n'a pas été faite, réessaie dans un moment.")
  })
  it('network error (0)', () => {
    expect(refusalToast(err(0), w)).toBe("pigtaild ne répond pas : l'action sur « Atlas web » n'a pas été faite.")
  })
  it.each([200, 409, 500])('a non-JSON body (%i)', (status) => {
    expect(refusalToast(new ApiError(status, 'x', undefined, undefined, true), w)).toBe("Réponse illisible de pigtaild : l'action sur « Atlas web » n'a pas été faite.")
  })
})

describe('copies outside the refusals', () => {
  it('no cell name: « la cellule du scope X » (D9-18)', () => {
    expect(refusalToast(err(409, 'SESSION_LIVE'), { title: X, scope: 'globex', action: 'launch' })).toBe(
      'Une session est déjà ouverte pour « Atlas web » (la cellule du scope globex).',
    )
    expect(refusalToast(err(403, 'BYPASS_REFUSED'), { title: X, scope: 'globex', action: 'launch' })).toBe(
      'La cellule du scope globex contourne les permissions : lancement refusé.',
    )
  })
  it('the cell name of the error details when the caller has none', () => {
    expect(refusalToast(err(409, 'SESSION_LIVE', { cell: 'tp' }), { title: X, scope: 'globex', action: 'launch' })).toBe(
      'Une session est déjà ouverte pour « Atlas web » (cellule tp).',
    )
  })
  it('an empty project list (capture)', () => {
    expect(COPY.noProjects).toBe('Aucun projet à proposer : crée-en un depuis la CLI.')
  })
  it('a rank changed (D23) and its undo (D9-10)', () => {
    expect(COPY.rankMoved(X)).toBe('Rang de « Atlas web » modifié')
    expect(COPY.rankRestored(X)).toBe('Rang de « Atlas web » rétabli')
    expect(COPY.undone('Rang de « Atlas web » rétabli')).toBe('Annulé · Rang de « Atlas web » rétabli')
  })
  it('an undo whose value changed since (D9-9)', () => {
    expect(COPY.undoStale(X)).toBe("Impossible d'annuler : « Atlas web » a changé depuis.")
  })
})
