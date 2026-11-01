/** The project camembert (t3 app.js pie): a ring and a wedge for done / total, named « N % fait » so it never rests on shape alone.
 *  Nothing for a project with no subject. */
export function Pie({ done, total, className }: { done: number; total: number; className?: string }) {
  if (total <= 0) return null
  const f = Math.min(1, Math.max(0, done / total))
  const a = f * 2 * Math.PI
  let wedge = null
  if (f >= 0.999) wedge = <circle cx="8" cy="8" r="4.25" fill="currentColor" />
  else if (f > 0) {
    const x = 8 + 4.25 * Math.sin(a)
    const y = 8 - 4.25 * Math.cos(a)
    wedge = <path d={`M8 8V3.75A4.25 4.25 0 ${f > 0.5 ? 1 : 0} 1 ${x.toFixed(2)} ${y.toFixed(2)}z`} fill="currentColor" />
  }
  return (
    <svg className={className ? `pie ${className}` : 'pie'} viewBox="0 0 16 16" role="img" aria-label={`${Math.round(f * 100)} % fait`}>
      <circle cx="8" cy="8" r="6.4" fill="none" stroke="currentColor" strokeWidth="1.3" />
      {wedge}
    </svg>
  )
}
