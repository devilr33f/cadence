import type { CreateOutBody } from '@/api/gen'
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { deleteDevice } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { deviceQueryOptions, devicesQueryOptions } from '@/lib/devices'

export interface Removal {
  name: string
  pending: boolean
  error?: string
  remove: () => Promise<void>
}

function isGone(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404
}

function enrolledMeanwhile(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409 && error.error === 'not_pending'
}

// an expired code's device has not enrolled and no longer can, yet it keeps its name taken. it is read once more
// first: the speaker may have used the code after the poll's last read, and one that enrolled stays (the page then
// follows it). the delete takes it only while it is pending, so one that enrolls between the read and the delete is
// refused (409 not_pending) and read again, to be followed too. one already gone counts as removed
export function useRemovePending(created: CreateOutBody, onRemoved: () => void): Removal {
  const api = useApi()
  const queryClient = useQueryClient()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()

  async function remove() {
    if (pending)
      return
    setPending(true)
    setError(undefined)
    const detail = deviceQueryOptions(api, created.id)
    try {
      const now = await queryClient.fetchQuery({ ...detail, staleTime: 0, retry: false })
      if (now.status === 'enrolled') {
        setPending(false)
        return
      }
      await deleteDevice({
        client: api,
        path: { id: created.id },
        query: { status: 'pending' },
        throwOnError: true,
      })
    }
    catch (failure) {
      if (enrolledMeanwhile(failure)) {
        await queryClient.invalidateQueries({ queryKey: detail.queryKey })
        setPending(false)
        return
      }
      if (!isGone(failure)) {
        setError(`${created.name} was not removed. ${describeError(failure)}`)
        setPending(false)
        return
      }
    }
    finally {
      // after any answer: one lost on its way may still have removed it
      void queryClient.invalidateQueries({ queryKey: devicesQueryOptions(api).queryKey })
    }
    queryClient.removeQueries({ queryKey: detail.queryKey })
    onRemoved()
  }

  return {
    name: created.name,
    pending,
    error,
    remove,
  }
}
