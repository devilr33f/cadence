import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/use-api'
import { deviceSettingsQueryOptions } from '@/lib/devices'
import { householdTimeZone, settingsQueryOptions } from '@/lib/time-zone'

export interface AlarmsTimeZone {
  // null until both sparse settings reads finish, as the rest of the dashboard's clocks
  timeZone: string | null
  // only a zone a scope explicitly set enables the device's schedule; the built-in utc does not (M6a-R6)
  configured?: boolean
}

function explicit(value: string | undefined): string | undefined {
  return value === ''
    ? undefined
    : value
}

// the alarms capability gets the device's own timeZone over the global one, never another device's
export function useAlarmsTimeZone(deviceId: string): AlarmsTimeZone {
  const api = useApi()
  const own = useQuery(deviceSettingsQueryOptions(api, deviceId))
  const global = useQuery(settingsQueryOptions(api))
  if (!own.isSuccess || !global.isSuccess)
    return { timeZone: null }
  const deviceZone = explicit(own.data.timeZone)
  const globalZone = explicit(global.data.timeZone)
  const zone = deviceZone ?? globalZone
  return {
    timeZone: householdTimeZone({ timeZone: zone }),
    configured: zone !== undefined,
  }
}
