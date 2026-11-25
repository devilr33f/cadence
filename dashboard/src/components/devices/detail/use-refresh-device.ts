import { useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '@/api/use-api'
import { deviceQueryOptions } from '@/lib/devices'

// after a change the device's detail is read again: the new generation, a secret's set flag, a command's effect
export function useRefreshDevice(deviceId: string): () => Promise<void> {
  const api = useApi()
  const queryClient = useQueryClient()
  return useCallback(
    () => queryClient.invalidateQueries({ queryKey: deviceQueryOptions(api, deviceId).queryKey }),
    [api, queryClient, deviceId],
  )
}
