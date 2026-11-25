import type { DeviceView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/use-api'
import { devicesQueryOptions } from '@/lib/devices'

const NONE: ReadonlyMap<string, string> = new Map()

function toNames(devices: DeviceView[]): ReadonlyMap<string, string> {
  return new Map(devices.map(d => [d.id, d.name]))
}

export function useDeviceNames(): ReadonlyMap<string, string> {
  const api = useApi()
  return useQuery({
    ...devicesQueryOptions(api),
    select: toNames,
  }).data ?? NONE
}
