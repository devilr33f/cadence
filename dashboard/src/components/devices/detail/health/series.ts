import type { MetricsOutBody, SeriesView } from '@/api/gen'

export interface Sample {
  at: number
  value: number
}

function attrsOf(series: SeriesView): Record<string, unknown> {
  return typeof series.attrs === 'object' && series.attrs !== null && !Array.isArray(series.attrs)
    ? series.attrs as Record<string, unknown>
    : {}
}

function samplesOf(series: SeriesView): Sample[] {
  const out: Sample[] = []
  for (const point of series.points ?? []) {
    const at = Date.parse(point.ts)
    if (!Number.isNaN(at) && Number.isFinite(point.value))
      out.push({ at, value: point.value })
  }
  return out
}

function sameAttrs(a: Record<string, unknown>, b: Readonly<Record<string, string>>): boolean {
  const keys = Object.keys(a)
  return keys.length === Object.keys(b).length && keys.every(key => a[key] === b[key])
}

// the series of a name whose attributes are exactly these: system.cpu.utilization with none is the total, a core's has
// its cpu
export function seriesOf(body: MetricsOutBody | undefined, name: string, attrs: Readonly<Record<string, string>> = {}): Sample[] {
  const series = body?.series?.find(s => s.name === name && sameAttrs(attrsOf(s), attrs))
  return series
    ? samplesOf(series)
    : []
}

// a name's series by one attribute's value (a filesystem's mountpoint), in the order the server sent them
export function seriesByAttr(body: MetricsOutBody | undefined, name: string, attr: string): Map<string, Sample[]> {
  const out = new Map<string, Sample[]>()
  for (const series of body?.series ?? []) {
    const value = attrsOf(series)[attr]
    if (series.name === name && typeof value === 'string')
      out.set(value, samplesOf(series))
  }
  return out
}

// at most `max` points: each run of consecutive points becomes one, at their mean time and value
export function downsample(samples: readonly Sample[], max: number): readonly Sample[] {
  if (samples.length <= max)
    return samples
  const run = Math.ceil(samples.length / max)
  const out: Sample[] = []
  for (let i = 0; i < samples.length; i += run) {
    const part = samples.slice(i, i + run)
    out.push({
      at: part.reduce((sum, s) => sum + s.at, 0) / part.length,
      value: part.reduce((sum, s) => sum + s.value, 0) / part.length,
    })
  }
  return out
}

// the device's usual step between points
function usualStep(samples: readonly Sample[]): number {
  const steps = samples.slice(1).map((s, i) => s.at - samples[i]!.at).sort((a, b) => a - b)
  return steps[Math.floor(steps.length / 2)] ?? 0
}

// the runs a line is drawn in: it breaks where the device sent nothing for over three of its usual steps (it was
// offline, or its metrics were off), rather than draw a slope it never reported
export function lineSegments(samples: readonly Sample[]): Sample[][] {
  const gap = usualStep(samples) * 3
  const runs: Sample[][] = []
  for (const [i, sample] of samples.entries()) {
    if (i === 0 || sample.at - samples[i - 1]!.at > gap)
      runs.push([])
    runs.at(-1)!.push(sample)
  }
  return runs
}

// the runs a chart draws: broken at the device's gaps in the raw points first, then each thinned in proportion to its
// length, so at most about `max` points are drawn and a gap shorter than a thinned run still shows
export function drawRuns(samples: readonly Sample[], max: number): Sample[][] {
  const runs = lineSegments(samples)
  if (samples.length <= max)
    return runs
  return runs.map(run => [
    ...downsample(run, Math.max(1, Math.floor(max * run.length / samples.length))),
  ])
}

// a mount's used share at each of its free points: 1 - free / limit, the limit of the same round or the latest before
// it; none where no positive limit came before
export function usedShare(free: readonly Sample[], limit: readonly Sample[]): Sample[] {
  const out: Sample[] = []
  let size: number | undefined
  let next = 0
  for (const point of free) {
    while (next < limit.length && limit[next]!.at <= point.at)
      size = limit[next++]!.value
    if (size !== undefined && size > 0) {
      out.push({
        at: point.at,
        value: Math.min(1, Math.max(0, 1 - point.value / size)),
      })
    }
  }
  return out
}
