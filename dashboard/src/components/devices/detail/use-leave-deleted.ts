import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect } from 'react'
import { useApi } from '@/api/use-api'
import { toast } from '@/components/ui/toast-manager'
import { forgetDevice } from '@/lib/devices'

export interface Deleted {
  id: string
  name: string
  // by another tab or an api caller: the feed said so
  elsewhere?: boolean
}

// runs once the page stopped showing the deleted device (its own delete, or the feed's word of one elsewhere): the
// commit that dropped it unsubscribed every query that reads it, so none can fetch it again as its queries are
// dropped. then back to the list, the focus on the content
export function useLeaveDeleted(deleted?: Deleted) {
  const api = useApi()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  useEffect(() => {
    if (!deleted)
      return
    void (async () => {
      await forgetDevice(queryClient, api, deleted.id)
      await navigate({ to: '/devices' })
      document.getElementById('main')?.focus()
      toast.add(deleted.elsewhere
        ? { type: 'info', title: `${deleted.name} was deleted elsewhere` }
        : { type: 'success', title: `${deleted.name} was deleted` })
    })()
  }, [deleted, api, queryClient, navigate])
}
