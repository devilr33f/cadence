import type { RouterContext } from '@/router-context'
import { redirect } from '@tanstack/react-router'
import { safeRedirect } from './redirect'
import { checkSession, sessionQueryOptions } from './session'

export interface LoginSearch {
  redirect?: string
}

export function validateLoginSearch(search: Record<string, unknown>): LoginSearch {
  return typeof search.redirect === 'string'
    ? { redirect: search.redirect }
    : {}
}

// every page but the login: the session the cache holds (the ssr server's, hydrated), else one GET; a 401 later on any
// query, or the feed's 1008, takes the reader back through returnToLogin
export async function requireSession({ context, location }: { context: RouterContext, location: { href: string } }) {
  const session = await context.queryClient.ensureQueryData(sessionQueryOptions(context.api))
  if (!session.authenticated) {
    throw redirect({
      to: '/login',
      search: { redirect: location.href },
    })
  }
}

// a reader who is signed in has nothing to do on the login: on to where they were going
export async function skipLoginWhenSignedIn({ context, search }: { context: RouterContext, search: LoginSearch }) {
  let signedIn = false
  try {
    signedIn = await checkSession(context.queryClient, context.api)
  }
  catch {
    // the form still works, and says what the server answers
  }
  if (signedIn) {
    throw redirect({
      href: safeRedirect(search.redirect),
    })
  }
}
