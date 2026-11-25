import type { MetricsOutBody } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { keepPreviousData } from '@tanstack/react-query'
import { listDeviceMetrics } from '@/api/gen'
import { getDeviceProcessesOptions, listDeviceMetricsQueryKey } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

const MINUTE = 60_000

// the windows a device's health shows, its metrics' 24 h retention the longest; each read again about as often as one
// of its chart's points spans, so a longer window asks less often for more
export const METRIC_WINDOWS = [
  {
    id: '15m',
    label: 'Last 15 minutes',
    ms: 15 * MINUTE,
    refreshMs: 10_000,
  },
  {
    id: '1h',
    label: 'Last hour',
    ms: 60 * MINUTE,
    refreshMs: 30_000,
  },
  {
    id: '6h',
    label: 'Last 6 hours',
    ms: 6 * 60 * MINUTE,
    refreshMs: 2 * MINUTE,
  },
  {
    id: '24h',
    label: 'Last 24 hours',
    ms: 24 * 60 * MINUTE,
    refreshMs: 5 * MINUTE,
  },
] as const

export type MetricWindow = typeof METRIC_WINDOWS[number]

export const DEFAULT_WINDOW: MetricWindow = METRIC_WINDOWS[1]

// each chart reads its own names: the server cuts a read at 50,000 points in name order, so one busy series (a core
// per cpu) cannot crowd another chart's out
export const METRIC_GROUPS = {
  cpu: ['system.cpu.utilization'],
  memory: ['system.memory.used', 'system.memory.limit'],
  storage: ['system.filesystem.free', 'system.filesystem.limit'],
  temperature: ['system.temperature'],
} as const

export type MetricGroup = keyof typeof METRIC_GROUPS

// the top processes come with each round of the device's metrics
export const PROCESSES_REFRESH_MS = 30_000

// a read with the span it covers and that window's name, so a chart held while another window loads keeps its own
// axis and says its own window
export interface MetricsRead {
  body: MetricsOutBody
  from: number
  to: number
  label: string
}

// a device's points of the group's names over the window: the key names the window, and each read asks from that long
// before it, so a window kept open stays that long
export function deviceMetricsQueryOptions(api: Client, deviceId: string, group: MetricGroup, window: MetricWindow) {
  const names = [...METRIC_GROUPS[group]]
  const { queryKey } = sharedKey({
    queryKey: listDeviceMetricsQueryKey({
      client: api,
      path: { id: deviceId },
      query: { name: names },
    }),
  })
  return {
    queryKey: [
      { ...queryKey[0], window: window.id },
    ] as const,
    queryFn: async ({ signal }: { signal: AbortSignal }): Promise<MetricsRead> => {
      const to = Date.now()
      const from = to - window.ms
      const { data } = await listDeviceMetrics({
        client: api,
        path: { id: deviceId },
        query: {
          name: names,
          since: new Date(from).toISOString(),
        },
        signal,
        throwOnError: true,
      })
      return {
        body: data,
        from,
        to,
        label: window.label,
      }
    },
    refetchInterval: window.refreshMs,
    // another window keeps the charts until its points land
    placeholderData: keepPreviousData,
  }
}

export function deviceProcessesQueryOptions(api: Client, deviceId: string) {
  return {
    ...sharedKey(getDeviceProcessesOptions({
      client: api,
      path: { id: deviceId },
    })),
    refetchInterval: PROCESSES_REFRESH_MS,
  }
}
