import type { Query, QueryClient, QueryKey } from '@tanstack/react-query'
import type { DeviceView } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/api/error'
import { deleteDevice, renameDevice, setDeviceRoom } from '@/api/gen'
import { getDeviceMediaOptions, getDeviceOptions, getDeviceSettingsOptions, listBoardsOptions, listDevicesOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

// the live feed's presence events keep the list current; a slow read stays as the floor, since the server may fail to
// publish one (device_presence_failed) and an idle speaker's flag would otherwise never be right again
export const DEVICES_FALLBACK_MS = 120_000

export function devicesQueryOptions(api: Client) {
  return {
    ...sharedKey(listDevicesOptions({ client: api })),
    refetchInterval: DEVICES_FALLBACK_MS,
  }
}

export function deviceQueryOptions(api: Client, id: string) {
  return sharedKey(getDeviceOptions({
    client: api,
    path: { id },
  }))
}

export function boardsQueryOptions(api: Client) {
  return sharedKey(listBoardsOptions({ client: api }))
}

export function deviceMediaQueryOptions(api: Client, id: string) {
  return sharedKey(getDeviceMediaOptions({
    client: api,
    path: { id },
  }))
}

// the device's own overrides; the global ones are settingsQueryOptions (lib/time-zone.ts)
export function deviceSettingsQueryOptions(api: Client, id: string) {
  return sharedKey(getDeviceSettingsOptions({
    client: api,
    path: { id },
  }))
}

// a device's name as the cache has it, for words about a device the page can no longer read
export function cachedDeviceName(queryClient: QueryClient, api: Client, id: string): string | undefined {
  return queryClient.getQueryData(deviceQueryOptions(api, id).queryKey)?.name
    ?? queryClient.getQueryData(devicesQueryOptions(api).queryKey)?.find(device => device.id === id)?.name
}

// a device that finished enrolling; only these can be online
export function isEnrolled(device: DeviceView): boolean {
  return device.status === 'enrolled'
}

// the devices list (each device's online flag with it), in the cache before the page renders, so the ssr server words
// the counts; a failure is the page's to show (and retry), not the navigation's to wait for
export async function prefetchDevices(queryClient: QueryClient, api: Client) {
  await queryClient.prefetchQuery({
    ...devicesQueryOptions(api),
    retry: false,
  })
}

// what a devices page reads first, in the cache before it renders; a failure is the page's to show (and retry), not the
// navigation's to wait for
export async function prefetchDeviceList(queryClient: QueryClient, api: Client) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...devicesQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...boardsQueryOptions(api),
      retry: false,
    }),
  ])
}

export async function prefetchDevicePage(queryClient: QueryClient, api: Client, id: string) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...deviceQueryOptions(api, id),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...deviceSettingsQueryOptions(api, id),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...deviceMediaQueryOptions(api, id),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...boardsQueryOptions(api),
      retry: false,
    }),
  ])
}

function names(params: unknown, id: string): boolean {
  return typeof params === 'object' && params !== null && Object.values(params).includes(id)
}

// every query that reads one device: the generated keys name it as a path parameter (its detail, settings, media,
// conversations, metrics, processes) or as a filter (its logs, memories, tasks, home assistant use). a device id is a
// uuid no other record shares, so any parameter equal to it means that device
export function readsDevice(queryKey: QueryKey, id: string): boolean {
  const key = queryKey[0] as { path?: unknown, query?: unknown } | undefined
  return names(key?.path, id) || names(key?.query, id)
}

async function forget(queryClient: QueryClient, api: Client, id: string, droppable: (query: Query) => boolean) {
  const filters = {
    predicate: (query: Query) => readsDevice(query.queryKey, id) && droppable(query),
  }
  await queryClient.cancelQueries(filters)
  queryClient.removeQueries(filters)
  const list = devicesQueryOptions(api).queryKey
  queryClient.setQueryData(list, devices => devices?.filter(device => device.id !== id))
  // the delete took the device's own memories and subscriptions with it, left the tasks it started without their
  // origin, and no signal makes it busy any more
  for (const operation of [
    'listDevices',
    'listMemories',
    'listTasks',
    'listSubscriptions',
    'getBusy',
  ]) {
    void queryClient.invalidateQueries({
      queryKey: [
        { _id: operation },
      ],
    })
  }
}

// once nothing on the page reads a deleted device: its queries are cancelled and dropped, so nothing asks the server
// for it again, and the list (the overview's counts too) goes on without it
export async function forgetDevice(queryClient: QueryClient, api: Client, id: string) {
  await forget(queryClient, api, id, () => true)
}

// a device deleted elsewhere that no page shows: what nothing observes is dropped. a query another page still observes
// (a filter on the device) keeps its last answer, since one dropped under its observer is fetched again at its next
// render
export async function forgetDeletedElsewhere(queryClient: QueryClient, api: Client, id: string) {
  await forget(queryClient, api, id, query => query.getObserversCount() === 0)
}

// called directly, not as mutations. the list, which the overview's counts read too, is marked stale after any answer
// (after a delete by forgetDevice, once the page let go of the device)
export function useDeviceActions(id: string) {
  const api = useApi()
  const queryClient = useQueryClient()
  const list = devicesQueryOptions(api).queryKey
  const refreshList = () => queryClient.invalidateQueries({ queryKey: list })
  return {
    // the answer is the device as get-device has it; after a failure it is read again, since a rename whose answer
    // was lost may still have landed
    async rename(name: string) {
      try {
        const { data } = await renameDevice({
          client: api,
          path: { id },
          body: { name },
          throwOnError: true,
        })
        queryClient.setQueryData(deviceQueryOptions(api, id).queryKey, data)
      }
      catch (failure) {
        void queryClient.invalidateQueries({ queryKey: deviceQueryOptions(api, id).queryKey })
        throw failure
      }
      finally {
        void refreshList()
      }
    },
    // the same as rename, for the device's room ("" for none)
    async setRoom(room: string) {
      try {
        const { data } = await setDeviceRoom({
          client: api,
          path: { id },
          body: { room },
          throwOnError: true,
        })
        queryClient.setQueryData(deviceQueryOptions(api, id).queryKey, data)
      }
      catch (failure) {
        void queryClient.invalidateQueries({ queryKey: deviceQueryOptions(api, id).queryKey })
        throw failure
      }
      finally {
        void refreshList()
      }
    },
    // a device the server no longer has counts as deleted
    async remove() {
      try {
        await deleteDevice({
          client: api,
          path: { id },
          throwOnError: true,
        })
      }
      catch (failure) {
        if (!(failure instanceof ApiError && failure.status === 404)) {
          // one lost on its way may still have deleted it
          void refreshList()
          throw failure
        }
      }
    },
  }
}

// the rooms the household's devices are in, by name, each once, for a room field's suggestions
export function knownRooms(devices: readonly Pick<DeviceView, 'room'>[] | undefined): string[] {
  const rooms = new Set<string>()
  for (const d of devices ?? []) {
    if (d.room)
      rooms.add(d.room)
  }
  return [...rooms].sort((a, b) => a.localeCompare(b))
}
