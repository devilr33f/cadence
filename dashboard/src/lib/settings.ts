import type { QueryClient } from '@tanstack/react-query'
import type { PutDeviceSettingsData, PutGlobalSettingsData, SettingsReply } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { putDeviceSettings, putGlobalSettings } from '@/api/gen'
import { deviceSettingsQueryOptions } from './devices'
import { settingsQueryOptions } from './time-zone'

// the global settings, or one device's overrides of them
export type SettingsScope = { kind: 'global' } | { kind: 'device', deviceId: string }

export const GLOBAL_SCOPE: SettingsScope = { kind: 'global' }

type ScopeQueryOptions = ReturnType<typeof settingsQueryOptions>

// the keys the scope itself sets (sparse). both scopes answer a SettingsReply, so one options type lets a component read
// either through one useQuery; only their keys' types differ
export function scopeQueryOptions(api: Client, scope: SettingsScope): ScopeQueryOptions {
  return scope.kind === 'global'
    ? settingsQueryOptions(api)
    : deviceSettingsQueryOptions(api, scope.deviceId) as unknown as ScopeQueryOptions
}

// the tab's global puts, one at a time per query client: each reads the set again only once the one before it landed,
// so two saves never start from the same set and undo each other
const globalPuts = new WeakMap<QueryClient, Promise<unknown>>()

function inTurn(queryClient: QueryClient, put: () => Promise<void>): Promise<void> {
  const turn = (globalPuts.get(queryClient) ?? Promise.resolve()).then(put, put)
  globalPuts.set(queryClient, turn)
  return turn
}

// what the server derives from the global set and other pages show: the mcp servers it runs and their tools (the mcp
// setting applies live), hermes's configured instance (hermes) and home assistant's (homeAssistant); no topic says
// when a setting changed them
const GLOBAL_DEPENDENTS = [
  'listMcpServers',
  'listMcpTools',
  'listTaskKinds',
  'getHomeassistant',
]

// sets one key of a scope (null removes it) and puts the server's answer in the cache. a device's put is a patch; the
// global put replaces the whole set (the server's PutGlobal), so it carries the set as the server holds it now, read
// again first, with the one change
export async function putSetting(api: Client, queryClient: QueryClient, scope: SettingsScope, key: string, value: unknown) {
  if (scope.kind === 'device') {
    const { data } = await putDeviceSettings({
      client: api,
      path: { id: scope.deviceId },
      // the generated type has no null for the object keys; the server takes one for every key
      body: { [key]: value } as PutDeviceSettingsData['body'],
      throwOnError: true,
    })
    queryClient.setQueryData<SettingsReply>(deviceSettingsQueryOptions(api, scope.deviceId).queryKey, data)
    return
  }
  await inTurn(queryClient, async () => {
    const options = settingsQueryOptions(api)
    // a read already on its way (a focus refetch) may predate the save before this one: fetchQuery would join it
    await queryClient.cancelQueries({ queryKey: options.queryKey })
    const current = await queryClient.fetchQuery({ ...options, staleTime: 0, retry: false })
    const { data } = await putGlobalSettings({
      client: api,
      body: { ...current, [key]: value } as PutGlobalSettingsData['body'],
      throwOnError: true,
    })
    queryClient.setQueryData<SettingsReply>(options.queryKey, data)
    for (const id of GLOBAL_DEPENDENTS) {
      void queryClient.invalidateQueries({
        queryKey: [
          { _id: id },
        ],
      })
    }
  })
}
