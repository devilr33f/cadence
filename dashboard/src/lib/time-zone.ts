import type { SettingsReply } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQuery } from '@tanstack/react-query'
import { getGlobalSettingsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

// the server's own default while the global setting is unset
const DEFAULT_TIME_ZONE = 'UTC'

export function settingsQueryOptions(api: Client) {
  return sharedKey(getGlobalSettingsOptions({ client: api }))
}

// a zone this runtime's Intl does not know falls back to utc on the ssr server and in the browser alike
export function householdTimeZone(settings: SettingsReply): string {
  const zone = settings.timeZone ?? DEFAULT_TIME_ZONE
  try {
    new Intl.DateTimeFormat('en-GB', { timeZone: zone }).format()
    return zone
  }
  catch {
    return DEFAULT_TIME_ZONE
  }
}

// the household's zone for every time on the page; null until the setting loads, so the ssr server and the browser
// both render placeholders rather than two different zones
export function useTimeZone(): string | null {
  const api = useApi()
  const { data } = useQuery({
    ...settingsQueryOptions(api),
    select: householdTimeZone,
  })
  return data ?? null
}
