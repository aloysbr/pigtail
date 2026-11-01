/** An age in minutes, the t10 buckets: « à l'instant », « N min », « N h », « N j », « N sem ». */
export function ago(m: number): string {
  if (m < 1) return "à l'instant"
  if (m < 60) return `${Math.floor(m)} min`
  if (m < 60 * 24) return `${Math.floor(m / 60)} h`
  if (m < 60 * 24 * 14) return `${Math.round(m / 1440)} j`
  return `${Math.round(m / 10080)} sem`
}

/** The age of an ISO date at now (ms); "" when the date is unreadable. */
export function ageOf(iso: string, now: number): string {
  const t = Date.parse(iso)
  if (!iso || Number.isNaN(t)) return ''
  return ago(Math.floor((now - t) / 60_000))
}
