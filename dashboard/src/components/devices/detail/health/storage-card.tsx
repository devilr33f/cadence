import type { CardProps } from './metric-cards'
import type { Sample } from './series'
import { bytesText, percentText } from '@/lib/units'
import { MetricCard, NoPoints } from './metric-card'
import { MetricChart } from './metric-chart'
import { seriesByAttr, usedShare } from './series'
import { useMetrics, words } from './use-metrics'

interface Mount {
  mountpoint: string
  free: number
  // its latest positive size; none when it never reported one
  limit?: number
  // its used share over the window
  used: Sample[]
}

// each mountpoint the board declares: its latest free space, its size, and its used share at each round
function mountsOf(free: Map<string, Sample[]>, limit: Map<string, Sample[]>): Mount[] {
  const out: Mount[] = []
  for (const [mountpoint, points] of free) {
    const latestFree = points.at(-1)?.value
    if (latestFree === undefined)
      continue
    const sizes = limit.get(mountpoint) ?? []
    const latestLimit = sizes.filter(s => s.value > 0).at(-1)?.value
    out.push({
      mountpoint,
      free: latestFree,
      limit: latestLimit,
      used: usedShare(points, sizes),
    })
  }
  return out
}

function MountMeter({ mountpoint, free, limit }: { mountpoint: string, free: number, limit: number }) {
  const used = Math.min(1, Math.max(0, (limit - free) / limit))
  return (
    <div
      role="meter"
      aria-label={`${mountpoint} used`}
      aria-valuemin={0}
      aria-valuemax={limit}
      aria-valuenow={limit - free}
      aria-valuetext={`${percentText(used)} used`}
      className="col-span-full h-2 overflow-hidden rounded-full bg-chart-1/15 sm:col-span-1"
    >
      <div className="h-full rounded-full bg-chart-1" style={{ width: `${used * 100}%` }} />
    </div>
  )
}

// a meter per mountpoint with its latest free space in words, and under it its used share over the window as a small
// chart; a mount that never reported its size gets its free space alone
export function StorageCard({ deviceId, window, timeZone }: CardProps) {
  const read = useMetrics(deviceId, 'storage', window)
  return (
    <MetricCard title="Storage" read={read}>
      {({ body, from, to, label }) => {
        const mounts = mountsOf(seriesByAttr(body, 'system.filesystem.free', 'mountpoint'), seriesByAttr(body, 'system.filesystem.limit', 'mountpoint'))
        if (mounts.length === 0)
          return <NoPoints />
        return (
          <ul className="flex flex-col gap-4">
            {mounts.map(({ mountpoint, free, limit, used }) => (
              <li key={mountpoint} className="flex flex-col gap-2">
                <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5 sm:grid-cols-[7rem_minmax(0,1fr)_auto]">
                  <span className="truncate font-mono text-xs">{mountpoint}</span>
                  <span className="text-right text-sm text-muted-foreground tabular-nums sm:order-last">
                    {limit === undefined
                      ? `${bytesText(free)} free; its size was not reported`
                      : `${bytesText(free)} free of ${bytesText(limit)}`}
                  </span>
                  {limit !== undefined && <MountMeter mountpoint={mountpoint} free={free} limit={limit} />}
                </div>
                {used.length > 0 && (
                  <MetricChart
                    title={`${mountpoint} used`}
                    window={words(label)}
                    samples={used}
                    from={from}
                    to={to}
                    domain={[0, 1]}
                    ticks={[0, 0.5, 1]}
                    format={percentText}
                    timeZone={timeZone}
                    size="small"
                  />
                )}
              </li>
            ))}
          </ul>
        )
      }}
    </MetricCard>
  )
}
