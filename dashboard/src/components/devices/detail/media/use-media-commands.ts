import type { ModesInBody, View } from '@/api/gen'
import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { jumpDeviceMusic, sendDeviceCommand, setDeviceMusicModes } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { deviceMediaQueryOptions } from '@/lib/devices'
import { deviceMusicQueryOptions } from '@/lib/music'

export type PlayerCommand = 'pause' | 'resume' | 'stop' | 'next' | 'previous'

export interface Failure {
  what: string
  message: string
}

// the player's presses: one at a time (a second press while one runs is ignored), the session and the queue read again
// after each, whatever the answer, since one lost on its way may still have landed
export function useMediaCommands(deviceId: string) {
  const api = useApi()
  const queryClient = useQueryClient()
  const [running, setRunning] = useState<string>()
  const [failure, setFailure] = useState<Failure>()
  // the state lags a render behind: two presses in one frame both read it unset
  const busyRef = useRef(false)
  const musicKey = deviceMusicQueryOptions(api, deviceId).queryKey

  const refresh = useCallback(() => Promise.all([
    queryClient.invalidateQueries({ queryKey: deviceMediaQueryOptions(api, deviceId).queryKey }),
    queryClient.invalidateQueries({ queryKey: deviceMusicQueryOptions(api, deviceId).queryKey }),
  ]), [api, queryClient, deviceId])

  async function run(what: string, call: () => Promise<View | void>) {
    if (busyRef.current)
      return
    busyRef.current = true
    setRunning(what)
    setFailure(undefined)
    try {
      const view = await call()
      if (view)
        queryClient.setQueryData(musicKey, view)
    }
    catch (error) {
      setFailure({
        what,
        message: describeError(error),
      })
    }
    finally {
      busyRef.current = false
      setRunning(undefined)
      void refresh()
    }
  }

  async function send(name: string, body: Record<string, unknown> = {}) {
    await sendDeviceCommand({
      client: api,
      path: { id: deviceId, cap: 'media.player', name },
      body,
      throwOnError: true,
    })
  }

  return {
    running,
    failure,
    command: (name: PlayerCommand) => run(name, () => send(name)),
    // a seek is the slider's: it waits on no other press and says its own failure, so it throws instead
    async seek(positionMs: number) {
      try {
        await send('seek', {
          positionMs: Math.round(positionMs),
        })
      }
      finally {
        await refresh()
      }
    },
    modes: (body: ModesInBody) => run(body.shuffle === undefined
      ? 'repeat'
      : 'shuffle', async () => (await setDeviceMusicModes({
      client: api,
      path: { id: deviceId },
      body,
      throwOnError: true,
    })).data),
    jump: (offset: number) => run('jump', async () => (await jumpDeviceMusic({
      client: api,
      path: { id: deviceId },
      body: { offset },
      throwOnError: true,
    })).data),
  }
}

export type MediaCommands = ReturnType<typeof useMediaCommands>
