import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/use-api'
import { devicesQueryOptions, isEnrolled } from '@/lib/devices'

export type SpeakerCounts
  = | { status: 'loading' }
    | { status: 'error', error: unknown, retry: () => void }
    | { status: 'ready', total: number, online: number, offline: number, enrolling: number }

// from the list alone, which says whether each device is linked now; read again on the devices topic's events
export function useSpeakerCounts(): SpeakerCounts {
  const api = useApi()
  const list = useQuery(devicesQueryOptions(api))
  if (list.isError) {
    return {
      status: 'error',
      error: list.error,
      retry: () => void list.refetch(),
    }
  }
  if (!list.data)
    return { status: 'loading' }
  const enrolled = list.data.filter(isEnrolled)
  const online = enrolled.filter(device => device.online).length
  return {
    status: 'ready',
    total: list.data.length,
    online,
    offline: enrolled.length - online,
    enrolling: list.data.length - enrolled.length,
  }
}
