import type { MetricWindow } from '@/lib/metrics'
import { bytesText, percentText, temperatureText } from '@/lib/units'
import { niceDomain, niceTicks } from './chart-scale'
import { Figure, MetricCard, NoPoints } from './metric-card'
import { MetricChart } from './metric-chart'
import { seriesOf } from './series'
import { useMetrics, words } from './use-metrics'

export interface CardProps {
  deviceId: string
  window: MetricWindow
  timeZone: string | null
}

// the total over every core, 0 to 100%
export function CpuCard({ deviceId, window, timeZone }: CardProps) {
  const read = useMetrics(deviceId, 'cpu', window)
  return (
    <MetricCard title="CPU" read={read}>
      {({ body, from, to, label }) => {
        const samples = seriesOf(body, 'system.cpu.utilization')
        if (samples.length === 0)
          return <NoPoints />
        return (
          <>
            <Figure value={percentText(samples.at(-1)!.value)} note="of all cores" />
            <MetricChart title="CPU" window={words(label)} samples={samples} from={from} to={to} domain={[0, 1]} ticks={[0, 0.5, 1]} format={percentText} timeZone={timeZone} />
          </>
        )
      }}
    </MetricCard>
  )
}

// what is used, up to what there is: the axis reaches the limit
export function MemoryCard({ deviceId, window, timeZone }: CardProps) {
  const read = useMetrics(deviceId, 'memory', window)
  return (
    <MetricCard title="Memory" read={read}>
      {({ body, from, to, label }) => {
        const samples = seriesOf(body, 'system.memory.used')
        if (samples.length === 0)
          return <NoPoints />
        // a limit of zero is none: the axis then reaches what was used
        const reported = seriesOf(body, 'system.memory.limit').at(-1)?.value
        const limit = reported !== undefined && reported > 0
          ? reported
          : undefined
        const top = limit ?? niceDomain(0, Math.max(...samples.map(s => s.value)))[1]
        return (
          <>
            <Figure
              value={bytesText(samples.at(-1)!.value)}
              note={limit === undefined
                ? 'used'
                : `used of ${bytesText(limit)}`}
            />
            <MetricChart title="Memory used" window={words(label)} samples={samples} from={from} to={to} domain={[0, top]} ticks={[0, top / 2, top]} format={bytesText} timeZone={timeZone} />
          </>
        )
      }}
    </MetricCard>
  )
}

// a board without a sensor sends none
export function TemperatureCard({ deviceId, window, timeZone }: CardProps) {
  const read = useMetrics(deviceId, 'temperature', window)
  return (
    <MetricCard title="Temperature" read={read}>
      {({ body, from, to, label }) => {
        const samples = seriesOf(body, 'system.temperature')
        if (samples.length === 0)
          return <NoPoints>No temperature in this window: the board may have no sensor.</NoPoints>
        const values = samples.map(s => s.value)
        const domain = niceDomain(Math.min(...values), Math.max(...values))
        return (
          <>
            <Figure value={temperatureText(samples.at(-1)!.value)} />
            <MetricChart title="Temperature" window={words(label)} samples={samples} from={from} to={to} domain={domain} ticks={niceTicks(...domain)} format={temperatureText} timeZone={timeZone} />
          </>
        )
      }}
    </MetricCard>
  )
}
