/** A test-only oklch() reader: the measures of tokens-measure.test.ts are made on the ROUNDED values of palette.css,
 *  never on the engine (palette.ref.cjs). Same maths as the engine (Björn Ottosson's OKLab, sRGB D65): a second,
 *  independent reader, so a rounding or a drift that breaks a contrast or a ΔE turns the test red. Not used at runtime. */

export interface Oklch {
  L: number
  C: number
  H: number
  a: number
}

/** Parses `oklch(L C H)` or `oklch(L C H / a)`; throws on anything else (a token must be a frozen literal). */
export function parseOklch(v: string): Oklch {
  const m = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*(?:\/\s*([\d.]+)\s*)?\)$/.exec(v.trim())
  if (!m) throw new Error(`not an oklch() literal: ${v}`)
  return { L: +m[1], C: +m[2], H: +m[3], a: m[4] === undefined ? 1 : +m[4] }
}

const clamp01 = (x: number) => Math.min(1, Math.max(0, x))

/** OKLCH → linear sRGB, clamped to the gamut (what a screen shows). */
export function toLinear({ L, C, H }: Oklch): [number, number, number] {
  const h = (H * Math.PI) / 180
  const a = C * Math.cos(h)
  const b = C * Math.sin(h)
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3
  return [
    clamp01(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s),
    clamp01(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s),
    clamp01(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s),
  ]
}

/** Linear sRGB → OKLab [L, a, b]. */
function toLab([r, g, b]: [number, number, number]): [number, number, number] {
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b)
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b)
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b)
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ]
}

/** WCAG relative luminance. */
export function luminance(c: Oklch): number {
  const [r, g, b] = toLinear(c)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** WCAG contrast ratio of two opaque colours. */
export function contrast(x: Oklch, y: Oklch): number {
  const p = luminance(x)
  const q = luminance(y)
  return (Math.max(p, q) + 0.05) / (Math.min(p, q) + 0.05)
}

/** ΔE in OKLab (Euclidean, on the in-gamut colours); 0.02 ≈ just noticeable. */
export function deltaE(x: Oklch, y: Oklch): number {
  const p = toLab(toLinear(x))
  const q = toLab(toLinear(y))
  return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2])
}
