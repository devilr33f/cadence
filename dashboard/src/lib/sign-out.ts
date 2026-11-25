import type { QueryClient } from '@tanstack/react-query'
import type { AnyRouter } from '@tanstack/react-router'
import type { Client } from '@/api/gen/client'
import { ApiError } from '@/api/error'
import { deleteSession } from '@/api/gen'

// a 401 on any query, or a feed the server closed for an ended session: to the login with the way back, then nothing of
// the session kept; the pages unmount first, so no query refetches into the cleared cache, and the many 401s of one
// page lead to one navigation
export function createReturnToLogin(getRouter: () => AnyRouter, queryClient: QueryClient) {
  let leaving = false
  return async () => {
    const router = getRouter()
    const { pathname, href } = router.state.location
    if (leaving || pathname === '/login')
      return
    leaving = true
    try {
      await router.navigate({
        to: '/login',
        search: { redirect: href },
      })
    }
    finally {
      queryClient.clear()
      leaving = false
    }
  }
}

// the owner's sign-out: the session ends on the server (a 401 means it already had), then the login; a failure leaves
// the reader signed in and is the caller's to say
export async function signOut({ api, router, queryClient }: { api: Client, router: AnyRouter, queryClient: QueryClient }) {
  try {
    await deleteSession({ client: api, throwOnError: true })
  }
  catch (error) {
    if (!(error instanceof ApiError && error.status === 401))
      throw error
  }
  await router.navigate({ to: '/login' })
  queryClient.clear()
}
