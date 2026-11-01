import type { CSSProperties } from 'react'
import { clearFilters, setUi, useUi } from '@/state/ui'
import { Icon } from './StatusIcon'

/** What the list says when nothing is left to show (spec § États vides). */
export function EmptyState() {
  const query = useUi((s) => s.query.trim())
  const areaF = useUi((s) => s.areaF)
  const statusF = useUi((s) => s.statusF)
  const projectF = useUi((s) => s.projectF)
  if (areaF && !query && !statusF) {
    return (
      <div className="empty6">
        <span className="empty-ic">
          <Icon name="search" />
        </span>
        <h2>Aucun sujet dans l'aire {areaF.name}</h2>
        <p>Cette aire est vide pour l'instant ; ses sujets apparaîtront ici.</p>
        <div className="hint">
          <button type="button" className="btnl" onClick={() => setUi({ areaF: null })}>
            Effacer le filtre
          </button>
        </div>
      </div>
    )
  }
  if (query || areaF || projectF || statusF) {
    return (
      <div className="empty6">
        <span className="empty-ic">
          <Icon name="search" />
        </span>
        <h2>Aucun sujet ne correspond</h2>
        <p>{query ? `Aucun titre ne contient « ${query} » dans le backlog.` : 'Aucun sujet ne correspond à ces filtres.'}</p>
        <div className="hint">
          {query ? (
            <button type="button" className="btnl" onClick={() => setUi({ query: '' })}>
              Effacer la recherche
            </button>
          ) : null}
          <button type="button" className="btnl" onClick={clearFilters}>
            Effacer tous les filtres
          </button>
        </div>
      </div>
    )
  }
  return (
    <div className="empty6">
      <span className="empty-ic">
        <Icon name="list" />
      </span>
      <h2>Le backlog est vide</h2>
      <p>Aucun sujet dans les scopes affichés.</p>
    </div>
  )
}

/** The silhouette of the list: a group header, then rows (title bar width, fixed so the page never reshuffles). */
const SKELETON: ({ g: string; n: string } | { w: string })[] = [
  { g: '22%', n: '4%' },
  { w: '62%' },
  { w: '45%' },
  { w: '71%' },
  { w: '38%' },
  { g: '16%', n: '3%' },
  { w: '55%' },
  { w: '66%' },
  { w: '42%' },
]

/** Skeleton shaped like the list while the first read is pending: group headers, then rows of status dot, title and age. */
export function Skeleton({ label = 'Chargement du backlog' }: { label?: string }) {
  return (
    <div className="ll6" aria-busy="true" aria-label={label}>
      {SKELETON.map((r, i) =>
        'g' in r ? (
          <div key={i} className="skel-g" aria-hidden="true">
            <i className="skel-b skel-name" style={{ '--w': r.g } as CSSProperties} />
            <i className="skel-b skel-n" style={{ '--w': r.n } as CSSProperties} />
          </div>
        ) : (
          <div key={i} className="skel" aria-hidden="true">
            <i className="skel-dot" />
            <i className="skel-b skel-t" style={{ '--w': r.w } as CSSProperties} />
            <i className="skel-b skel-age" />
          </div>
        ),
      )}
    </div>
  )
}
