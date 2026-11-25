import type { Client } from '@/api/gen/client'
import { getHomeassistantOptions, listHomeassistantAreasOptions, listHomeassistantEntitiesOptions, listHomeassistantUsageOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

// the server's most a read: a household larger than this is narrowed by a search, an area or a domain
export const ENTITIES_LIMIT = 500

// home assistant's entity domains for the domain filter: its entity platforms (homeassistant.const.Platform) and the
// domains of its helpers and automation, fixed here since no route lists the domains a home has; a domain a read
// returns joins them, and the server filters by any domain it is given
export const HA_DOMAINS: readonly string[] = [
  'ai_task',
  'alarm_control_panel',
  'alert',
  'assist_satellite',
  'automation',
  'binary_sensor',
  'button',
  'calendar',
  'camera',
  'climate',
  'conversation',
  'counter',
  'cover',
  'date',
  'datetime',
  'device_tracker',
  'event',
  'fan',
  'geo_location',
  'group',
  'humidifier',
  'image',
  'image_processing',
  'input_boolean',
  'input_button',
  'input_datetime',
  'input_number',
  'input_select',
  'input_text',
  'lawn_mower',
  'light',
  'lock',
  'media_player',
  'notify',
  'number',
  'person',
  'remote',
  'scene',
  'schedule',
  'script',
  'select',
  'sensor',
  'siren',
  'stt',
  'sun',
  'switch',
  'text',
  'time',
  'timer',
  'todo',
  'tts',
  'update',
  'vacuum',
  'valve',
  'wake_word',
  'water_heater',
  'weather',
  'zone',
]

// no topic carries home assistant: the status and the states are read again on this beat while the page is open
export const HOME_ASSISTANT_REFRESH_MS = 30_000

export function homeAssistantQueryOptions(api: Client) {
  return sharedKey(getHomeassistantOptions({ client: api }))
}

export function areasQueryOptions(api: Client) {
  return sharedKey(listHomeassistantAreasOptions({ client: api }))
}

// narrowed on the server by a search (a name, an id or an alias), an area id and a domain, before its limit
export function entitiesQueryOptions(api: Client, filter: { q: string, area: string, domain: string }) {
  return sharedKey(listHomeassistantEntitiesOptions({
    client: api,
    query: {
      ...(filter.q !== '' && { q: filter.q }),
      ...(filter.area !== '' && { area: filter.area }),
      ...(filter.domain !== '' && { domain: filter.domain }),
      limit: ENTITIES_LIMIT,
    },
  }))
}

// the household's most used entities, or one speaker's
export function usageQueryOptions(api: Client, deviceId: string) {
  return sharedKey(listHomeassistantUsageOptions({
    client: api,
    ...(deviceId !== '' && {
      query: { device: deviceId },
    }),
  }))
}
