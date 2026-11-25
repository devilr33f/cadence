import { useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { sendDeviceCommand } from '@/api/gen'
import { useApi } from '@/api/use-api'

// the keys the pad sends, input.remote's key enum. no volume_up: the dashboard never raises a device's level from a
// button press it can repeat by accident (rule 7); the tv's own remote and the media card's level do that
export type RemoteKey = 'up' | 'down' | 'left' | 'right' | 'ok' | 'back' | 'home' | 'menu' | 'play_pause' | 'previous'
  | 'next' | 'volume_down' | 'volume_mute'

export type KeyResult = { status: 'idle' } | { status: 'pending', key: RemoteKey } | { status: 'sent', key: RemoteKey }
  | { status: 'failed', key: RemoteKey, message: string }

// one key at a time, as the screen would get them from a remote: a press while one is on its way waits for none
export function useRemoteKey(deviceId: string) {
  const api = useApi()
  const [result, setResult] = useState<KeyResult>({ status: 'idle' })
  const busyRef = useRef(false)
  async function press(key: RemoteKey) {
    if (busyRef.current)
      return
    busyRef.current = true
    setResult({ status: 'pending', key })
    try {
      await sendDeviceCommand({
        client: api,
        path: { id: deviceId, cap: 'input.remote', name: 'key' },
        body: { key },
        throwOnError: true,
      })
      setResult({ status: 'sent', key })
    }
    catch (failure) {
      setResult({
        status: 'failed',
        key,
        message: describeError(failure),
      })
    }
    finally {
      busyRef.current = false
    }
  }
  return { result, press }
}
