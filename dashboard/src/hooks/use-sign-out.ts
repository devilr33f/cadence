import { useRouteContext, useRouter } from '@tanstack/react-router'
import { useCallback } from 'react'
import { describeError } from '@/api/describe-error'
import { toast } from '@/components/ui/toast-manager'
import { signOut } from '@/lib/sign-out'

// the account menu's sign-out; a failure leaves the reader signed in, and says why in codes
export function useSignOut(): () => void {
  const router = useRouter()
  const { api, queryClient } = useRouteContext({ from: '__root__' })
  return useCallback(() => {
    signOut({ api, router, queryClient }).catch((error: unknown) => {
      toast.add({
        type: 'error',
        title: 'Sign-out failed',
        description: describeError(error),
      })
    })
  }, [api, router, queryClient])
}
