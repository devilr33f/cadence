import { createFileRoute } from '@tanstack/react-router'
import { LoginPage } from '@/components/login/login-page'
import { skipLoginWhenSignedIn, validateLoginSearch } from '@/lib/auth-guard'

export const Route = createFileRoute('/login')({
  validateSearch: validateLoginSearch,
  beforeLoad: skipLoginWhenSignedIn,
  head: () => ({
    meta: [
      { title: 'Sign in · Cadence' },
    ],
  }),
  component: LoginPage,
})
