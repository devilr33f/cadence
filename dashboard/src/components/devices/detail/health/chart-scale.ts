import { zoneOffsetMs } from '@/lib/zoned-time'

// a step of 1, 2 or 5 times a power of ten, the nearest to `raw`
function niceStep(raw: number): number {
  const base = 10 ** Math.floor(Math.log10(raw))
  const ratio = raw / base
  if (ratio >= Math.SQRT2 * 5)
    return base * 10
  if (ratio >= Math.sqrt(10))
    return base * 5
  if (ratio >= Math.SQRT2)
    return base * 2
  return base
}

const clean = (n: number) => Number(n.toPrecision(12))

// about `count` ticks at round values within [min, max]
export function niceTicks(min: number, max: number, count = 3): number[] {
  if (!(max > min))
    return [min]
  const step = niceStep((max - min) / (count - 1))
  const ticks: number[] = []
  for (let k = Math.ceil(min / step); k * step <= max + step * 1e-9; k++)
    ticks.push(clean(k * step))
  return ticks
}

// the values' range widened to round ends; a flat line gets room on both sides
export function niceDomain(min: number, max: number): [number, number] {
  if (!(max > min))
    return [min - 1, max + 1]
  const step = niceStep((max - min) / 2)
  return [
    clean(Math.floor(min / step) * step),
    clean(Math.ceil(max / step) * step),
  ]
}

const MINUTE = 60_000
const HOUR = 60 * MINUTE

function timeStep(span: number): number {
  if (span <= 20 * MINUTE)
    return 5 * MINUTE
  if (span <= 90 * MINUTE)
    return 15 * MINUTE
  if (span <= 8 * HOUR)
    return 2 * HOUR
  return 6 * HOUR
}

// ticks on the household's clock (quarter hours, every 2 or 6 hours of its day) between from and to
export function timeTicks(from: number, to: number, zone: string): number[] {
  const step = timeStep(to - from)
  const offset = zoneOffsetMs(from, zone)
  const ticks: number[] = []
  for (let wall = Math.ceil((from + offset) / step) * step; wall - offset <= to; wall += step)
    ticks.push(wall - offset)
  return ticks
}
