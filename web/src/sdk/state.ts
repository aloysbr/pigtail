import type { SdkEvent, SdkState } from '@/api/types'

/** D18: the label of a state. The label carries the state; the colour only accompanies it (R36). */
export const STATE_LABEL: Record<SdkState, string> = {
  starting: 'Démarre',
  running: 'Travaille',
  waiting: 'Attend ta réponse',
  idle: 'Tour terminé',
  ended: 'Terminée',
  failed: 'Échec',
  lost: 'Perdue',
}

export function stateLabel(state: string): string {
  return STATE_LABEL[state as SdkState] ?? state
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '')

/** The one-line reading of a journal event (the host's `data` is opaque to the daemon, read here defensively). */
export function eventText(e: SdkEvent): string {
  const d = e.data ?? {}
  switch (e.kind) {
    case 'init':
      return 'Session démarrée'
    case 'state':
      return stateLabel(str(d.state))
    case 'assistant_text':
      return str(d.text)
    case 'user_message':
      return `Ta réponse : ${str(d.text)}`
    case 'tool_use':
      return `Outil · ${str(d.name)}`
    case 'tool_result':
      return d.is_error ? "Résultat d'outil · erreur" : "Résultat d'outil"
    case 'request_opened':
      return `Demande · ${str(d.tool) || str(d.kind)}`
    case 'request_answered':
      return d.behavior === 'deny' ? 'Demande refusée' : 'Demande acceptée'
    case 'result':
      // distinct from the idle state's « Tour terminé » (D18): the two lines follow each other in the thread
      return 'Fin du tour'
    case 'error':
      return `Erreur : ${str(d.message)}`
    case 'rate_limit':
      return "Limite d'usage"
    default:
      return e.kind
  }
}
