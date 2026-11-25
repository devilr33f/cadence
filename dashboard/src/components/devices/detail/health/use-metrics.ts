import type { MetricGroup, MetricWindow } from '@/lib/metrics'
import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/use-api'
import { deviceMetricsQueryOptions } from '@/lib/metrics'

export function useMetrics(deviceId: string, group: MetricGroup, window: MetricWindow) {
  const api = useApi()
  return useQuery(deviceMetricsQueryOptions(api, deviceId, group, window))
}

// the window a read covers, in words: last hour
export const words = (label: string) => label.toLowerCase()
