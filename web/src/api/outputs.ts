import { useQuery } from '@tanstack/react-query'
import { useApi } from './client'
import { subjectRoute } from './subject'
import { REFETCH_MS } from './queries'
import type { Output, OutputGroup } from './types'

/** The type chip of each output kind (G6, t10 `app.js:1127`); the closed list of pkg/pigtail OutputKinds. */
export const OUTPUT_KIND_LABEL: Record<string, string> = {
  pr: 'PR',
  mockup: 'Maquette',
  report: 'Rapport',
  capture: "Capture d'écran",
  html: 'Page',
  pdf: 'PDF',
  server: 'Serveur',
  clip: 'Clip',
  link: 'Lien',
  file: 'Fichier',
}

/** The chip of a kind; an unknown kind shows as it is stored, never dropped. */
export function kindLabel(kind: string): string {
  return OUTPUT_KIND_LABEL[kind] ?? kind
}

/** The outputs the sheet lists: not purged, in registration order. */
export function shownOutputs(outputs: readonly Output[]): Output[] {
  return outputs.filter((o) => o.purged_at == null).sort((a, b) => a.seq - b.seq)
}

/** The query key of a subject's outputs; its groups live under it, so invalidating it re-reads both. */
export function outputsKey(id: string) {
  return ['outputs', id] as const
}

/** The query key of a subject's deliverables grouped by rounds. */
export function outputGroupsKey(id: string) {
  return ['outputs', id, 'groups'] as const
}

/** The outputs of the open subject (GET /subjects/{id}/outputs), re-read with the backlog. */
export function useSubjectOutputs(id: string) {
  const get = useApi()
  return useQuery({ queryKey: outputsKey(id), queryFn: () => get<Output[]>(subjectRoute(id, 'outputs')) })
}

/** The deliverables of the open subject by rounds (GET /subjects/{id}/outputs/groups, D2), re-read with the backlog. */
export function useOutputGroups(id: string) {
  const get = useApi()
  return useQuery({
    queryKey: outputGroupsKey(id),
    queryFn: () => get<OutputGroup[]>(`${subjectRoute(id, 'outputs')}/groups`),
    refetchInterval: REFETCH_MS,
  })
}

/** « Maquette · 21 tours », « Rapport · 1 tour »: the kind's chip and the round count. */
export function groupLabel(g: Pick<OutputGroup, 'kind' | 'tours'>): string {
  const n = g.tours.length
  return `${kindLabel(g.kind)} · ${n} ${n === 1 ? 'tour' : 'tours'}`
}
