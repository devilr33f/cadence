import { useCallback } from 'react'
import { sendDeviceCommand } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { useRefreshDevice } from '../use-refresh-device'

export type AlarmCommand = 'set_timer' | 'cancel_timer' | 'set_reminder' | 'cancel_reminder' | 'snooze' | 'stop_ring'

// the dashboard's sessionless alarms commands; the new state is read after the device answers
export function useAlarmCommand(deviceId: string) {
  const api = useApi()
  const refresh = useRefreshDevice(deviceId)
  return useCallback(async (name: AlarmCommand, body: Record<string, unknown>) => {
    try {
      await sendDeviceCommand({
        client: api,
        path: { id: deviceId, cap: 'alarms', name },
        body,
        throwOnError: true,
      })
    }
    catch (error) {
      // a refusal can follow an effect whose answer was lost: the next detail still tells the truth
      void refresh()
      throw error
    }
    await refresh()
  }, [api, deviceId, refresh])
}
