import type { QueryClient } from '@tanstack/react-query'
import type { ListenerPhase } from './listener-calibration'
import type { CalibrateBoostRequest, CalibrationView, GroupRequest, GroupView } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { applyGroupCalibration, calibrateGroup, createGroup, deleteGroup, updateGroup } from '@/api/gen'
import { getGroupOptions, listGroupsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'
import { devicesQueryOptions } from './devices'
import { runListenerCalibration } from './listener-calibration'
import { startRecorder } from './listener-recorder'

export function groupsQueryOptions(api: Client) {
  return sharedKey(listGroupsOptions({ client: api }))
}

export function groupQueryOptions(api: Client, id: string) {
  return sharedKey(getGroupOptions({
    client: api,
    path: { id },
  }))
}

// the list and the speakers' names (and their groups, for the picker) in the cache before the page renders; a failure
// is the page's to show (and retry)
export async function prefetchGroups(queryClient: QueryClient, api: Client) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...groupsQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...devicesQueryOptions(api),
      retry: false,
    }),
  ])
}

export async function prefetchGroup(queryClient: QueryClient, api: Client, id: string) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...groupQueryOptions(api, id),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...devicesQueryOptions(api),
      retry: false,
    }),
  ])
}

// group_changed refreshes the groups for every page that follows the topic; a change also refreshes them itself, since
// the page that made it may follow nothing else, and the devices too, which name their group
export function useGroupActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  const refresh = (ids: readonly string[]) => Promise.all(ids.map(id => queryClient.invalidateQueries({
    queryKey: [
      { _id: id },
    ],
  })))
  return {
    // a save after any answer refreshes: one whose answer was lost may still have landed
    async save(id: string | undefined, body: GroupRequest): Promise<GroupView> {
      try {
        const { data } = id === undefined
          ? await createGroup({ client: api, body, throwOnError: true })
          : await updateGroup({
              client: api,
              path: { id },
              body,
              throwOnError: true,
            })
        return data
      }
      finally {
        void refresh([
          'listGroups',
          'getGroup',
          'listDevices',
          'getDevice',
        ])
      }
    },
    // every member plays a chirp and the leader solves their latencies; an ok one stores the trims, which the group's
    // read then shows. a refusal (playing, gated, no_leader, ...) rejects with its code
    // boost plays the named members' chirps louder (still under their own volume, which never changes: rule 7)
    async calibrate(id: string, boost: CalibrateBoostRequest[] = []): Promise<CalibrationView> {
      try {
        const { data } = await calibrateGroup({
          client: api,
          path: { id },
          ...(boost.length > 0 && {
            body: { boost },
          }),
          throwOnError: true,
        })
        return data
      }
      finally {
        void refresh(['listGroups', 'getGroup'])
      }
    },
    // listener mode: this browser's microphone, where the owner stands, hears every member's chirp; only the arrivals
    // it measured are posted (rule 8). called from the owner's press: the recording starts inside it
    async calibrateByListening(id: string, onPhase?: (phase: ListenerPhase) => void, boost: CalibrateBoostRequest[] = []): Promise<CalibrationView> {
      try {
        return await runListenerCalibration({
          record: startRecorder,
          calibrate: async () => (await calibrateGroup({
            client: api,
            path: { id },
            body: boost.length > 0
              ? { mode: 'listener', boost }
              : { mode: 'listener' },
            throwOnError: true,
          })).data,
          apply: async (calibrationId, members) => (await applyGroupCalibration({
            client: api,
            path: { id, calibrationId },
            body: { members },
            throwOnError: true,
          })).data,
          now: () => performance.now(),
          sleep: ms => new Promise(resolve => setTimeout(resolve, ms)),
        }, onPhase)
      }
      finally {
        void refresh(['listGroups', 'getGroup'])
      }
    },
    // the group's own read is left alone: the page that shows it leaves, and a read of it now would only be refused
    async remove(id: string) {
      await deleteGroup({
        client: api,
        path: { id },
        throwOnError: true,
      })
      void refresh(['listGroups', 'listDevices', 'getDevice'])
    },
  }
}
