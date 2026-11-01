import { NO_AREA, type Row } from '@/domain/group'

/** The area and project of a row, with the « Sans aire » / « Sans projet » of the list. */
export function areaProject(row: Row): string {
  return `${row.area || NO_AREA} › ${row.projectName}`
}

/** The sheet's trail « scope › aire › projet ». */
export function Breadcrumb({ row }: { row: Row }) {
  return (
    <span className="muted lp-trail" title={`${row.scope} › ${areaProject(row)}`}>
      {row.scope || '—'} › {areaProject(row)}
    </span>
  )
}
