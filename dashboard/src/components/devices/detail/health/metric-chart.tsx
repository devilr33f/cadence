import type { KeyboardEvent, PointerEvent } from 'react'
import type { Sample } from './series'
import { useState } from 'react'
import { clockText } from '@/lib/format-time'
import { timeTicks } from './chart-scale'
import { MetricTable } from './metric-table'
import { drawRuns } from './series'

// about this many points on a line at most: a chart a few hundred pixels wide shows no more. the server sends every
// point of the window (bounded by its 50,000-point read), and the chart averages runs of them down to this
export const MAX_POINTS = 240

const W = 1000
const H = 100

export interface MetricChartProps {
  // what the line is, for its summary and its table: CPU, Memory used
  title: string
  // the window in words: last hour
  window: string
  samples: readonly Sample[]
  from: number
  to: number
  domain: readonly [number, number]
  ticks: readonly number[]
  format: (value: number) => string
  timeZone: string | null
  // a small chart (a mount's under its meter) has a shorter plot
  size?: 'default' | 'small'
}

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n))

function nearest(points: readonly Sample[], at: number): number {
  let best = 0
  for (const [i, point] of points.entries()) {
    if (Math.abs(point.at - at) < Math.abs(points[best]!.at - at))
      best = i
  }
  return best
}

// a label on the time axis stays inside the plot at its ends
function edgeShift(percent: number): string {
  if (percent < 8)
    return '0'
  return percent > 92
    ? '-100%'
    : '-50%'
}

// one series over a window as a small area chart (DESIGN.md, Charts): a 2px line in the first series colour over a 10%
// wash of it, hairline gridlines, the latest point marked; the line breaks where the device sent nothing. the plot
// takes the keyboard: arrows move a readout along the points, which the pointer does too, and the points are a table
export function MetricChart({ title, window, samples, from, to, domain, ticks, format, timeZone, size = 'default' }: MetricChartProps) {
  const [active, setActive] = useState<number | null>(null)
  const [keyboard, setKeyboard] = useState(false)
  const runs = drawRuns(samples, MAX_POINTS)
  const points = runs.flat()
  const averaged = points.length < samples.length
  const plot = size === 'small'
    ? 'h-20'
    : 'h-32'
  const [min, max] = domain
  const x = (at: number) => clamp((at - from) / (to - from), 0, 1) * W
  const y = (value: number) => H - (clamp(value, min, max) - min) / (max - min) * H
  const values = samples.map(s => s.value)
  const latest = samples.at(-1)
  const summary = latest
    ? `${title} over the ${window}: latest ${format(latest.value)}, highest ${format(Math.max(...values))}, lowest ${format(Math.min(...values))}`
    : `${title} over the ${window}: no points`
  const shown = active === null
    ? undefined
    : points[active]
  const readout = shown && `${format(shown.value)} at ${clockText(new Date(shown.at).toISOString(), timeZone)}`
  const mark = shown ?? points.at(-1)

  function onPointerMove(event: PointerEvent<HTMLDivElement>) {
    const rect = event.currentTarget.getBoundingClientRect()
    if (points.length === 0 || rect.width === 0)
      return
    setKeyboard(false)
    setActive(nearest(points, from + (event.clientX - rect.left) / rect.width * (to - from)))
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const last = points.length - 1
    const moves: Record<string, (i: number) => number> = {
      ArrowLeft: i => Math.max(0, i - 1),
      ArrowRight: i => Math.min(last, i + 1),
      Home: () => 0,
      End: () => last,
    }
    const move = moves[event.key]
    if (!move || last < 0)
      return
    event.preventDefault()
    setKeyboard(true)
    setActive(i => move(i ?? last))
  }

  return (
    <figure className="flex flex-col gap-2">
      <div className="grid grid-cols-[3.75rem_minmax(0,1fr)] gap-x-2">
        <div aria-hidden="true" className={`relative ${plot}`}>
          {ticks.map(tick => (
            <span key={tick} className="absolute right-0 -translate-y-1/2 text-xs text-muted-foreground tabular-nums" style={{ top: `${y(tick)}%` }}>
              {format(tick)}
            </span>
          ))}
        </div>
        <div
          role="group"
          aria-roledescription="chart"
          aria-label={summary}
          tabIndex={0}
          className={`relative ${plot} touch-none`}
          onPointerMove={onPointerMove}
          onPointerLeave={() => !keyboard && setActive(null)}
          onFocus={() => {
            setKeyboard(true)
            setActive(points.length > 0
              ? points.length - 1
              : null)
          }}
          onBlur={() => setActive(null)}
          onKeyDown={onKeyDown}
        >
          <svg aria-hidden="true" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="absolute inset-0 size-full overflow-visible">
            {ticks.map(tick => (
              <line key={tick} x1={0} x2={W} y1={y(tick)} y2={y(tick)} className="stroke-border" strokeWidth={1} vectorEffect="non-scaling-stroke" />
            ))}
            {runs.map(run => run.length > 1 && (
              <g key={run[0]!.at}>
                <path
                  d={`M${x(run[0]!.at)},${H} ${run.map(p => `L${x(p.at)},${y(p.value)}`).join(' ')} L${x(run.at(-1)!.at)},${H}Z`}
                  className="fill-chart-1/10"
                />
                <path
                  d={run.map((p, i) => `${i === 0
                    ? 'M'
                    : 'L'}${x(p.at)},${y(p.value)}`).join(' ')}
                  className="fill-none stroke-chart-1"
                  strokeWidth={2}
                  strokeLinejoin="round"
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              </g>
            ))}
          </svg>
          {shown && (
            <span aria-hidden="true" className="pointer-events-none absolute inset-y-0 w-px bg-foreground/30" style={{ left: `${x(shown.at) / 10}%` }} />
          )}
          {mark && (
            <span
              aria-hidden="true"
              className="pointer-events-none absolute size-2.5 -translate-1/2 rounded-full bg-chart-1 ring-2 ring-card"
              style={{ left: `${x(mark.at) / 10}%`, top: `${y(mark.value)}%` }}
            />
          )}
          {shown && (
            <span
              aria-hidden="true"
              className="pointer-events-none absolute -top-1 z-10 flex items-baseline gap-1.5 rounded-md border bg-popover px-2 py-1 text-xs whitespace-nowrap shadow-md"
              style={{ left: `${x(shown.at) / 10}%`, translate: `${edgeShift(x(shown.at) / 10)} -100%` }}
            >
              <span className="text-sm font-semibold tabular-nums">{format(shown.value)}</span>
              <span className="text-muted-foreground tabular-nums">{clockText(new Date(shown.at).toISOString(), timeZone)}</span>
            </span>
          )}
          <p role="status" className="sr-only">
            {keyboard && readout
              ? readout
              : ''}
          </p>
        </div>
        <div />
        <div aria-hidden="true" className="relative mt-1 h-4">
          {timeTicks(from, to, timeZone ?? 'UTC').map((tick) => {
            const percent = x(tick) / 10
            return (
              <span
                key={tick}
                className="absolute text-xs text-muted-foreground tabular-nums"
                style={{
                  left: `${percent}%`,
                  translate: edgeShift(percent),
                }}
              >
                {clockText(new Date(tick).toISOString(), timeZone).slice(0, 5)}
              </span>
            )
          })}
        </div>
      </div>
      <MetricTable title={title} points={points} averaged={averaged} format={format} timeZone={timeZone} />
    </figure>
  )
}
