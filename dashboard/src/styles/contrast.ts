// wcag 2.x contrast for the oklch tokens in tokens.css (css color 4: oklch -> oklab -> linear srgb)

export interface Oklch {
  l: number
  c: number
  h: number
  alpha: number
}

type Rgb = readonly [number, number, number]

const OKLCH = /^oklch\(\s*([\d.]+)(%?)\s+([\d.]+)\s+([\d.]+)\s*(?:\/\s*([\d.]+)(%?)\s*)?\)$/

export function parseOklch(value: string): Oklch {
  const match = OKLCH.exec(value.trim())
  if (!match)
    throw new Error(`not an oklch colour: ${value}`)
  const [, l, lPercent, c, h, alpha, alphaPercent] = match
  return {
    l: Number(l) / (lPercent ? 100 : 1),
    c: Number(c),
    h: Number(h),
    alpha: alpha === undefined
      ? 1
      : Number(alpha) / (alphaPercent ? 100 : 1),
  }
}

function toLinearSrgb({ l, c, h }: Oklch): Rgb {
  const rad = (h * Math.PI) / 180
  const a = c * Math.cos(rad)
  const b = c * Math.sin(rad)
  const l1 = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m1 = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s1 = (l - 0.0894841775 * a - 1.291485548 * b) ** 3
  const clamp = (v: number) => Math.min(1, Math.max(0, v))
  return [
    clamp(4.0767416621 * l1 - 3.3077115913 * m1 + 0.2309699292 * s1),
    clamp(-1.2684380046 * l1 + 2.6097574011 * m1 - 0.3413193965 * s1),
    clamp(-0.0041960863 * l1 - 0.7034186147 * m1 + 1.707614701 * s1),
  ]
}

function encode(v: number) {
  return v <= 0.0031308
    ? 12.92 * v
    : 1.055 * v ** (1 / 2.4) - 0.055
}
function decode(v: number) {
  return v <= 0.04045
    ? v / 12.92
    : ((v + 0.055) / 1.055) ** 2.4
}

// a translucent colour is blended over what lies under it in gamma-encoded srgb, as browsers paint it
function paint(colour: Oklch, under: Rgb): Rgb {
  const top = toLinearSrgb(colour)
  if (colour.alpha >= 1)
    return top
  const mix = (i: 0 | 1 | 2) => decode(encode(top[i]) * colour.alpha + encode(under[i]) * (1 - colour.alpha))
  return [
    mix(0),
    mix(1),
    mix(2),
  ]
}

function luminance([r, g, b]: Rgb): number {
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

// the ratio of a foreground on a background; a translucent background lies on the backdrop (white by default)
export function contrast(foreground: Oklch, background: Oklch, backdrop?: Oklch): number {
  const bg = paint(background, backdrop
    ? toLinearSrgb(backdrop)
    : [1, 1, 1])
  const fg = paint(foreground, bg)
  const [hi, lo] = [
    luminance(fg),
    luminance(bg),
  ].sort((x, y) => y - x) as [number, number]
  return (hi + 0.05) / (lo + 0.05)
}
