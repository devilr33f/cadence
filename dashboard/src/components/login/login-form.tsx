import type { FormEvent } from 'react'
import { useRouteContext, useRouter } from '@tanstack/react-router'
import { LoaderCircleIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { createSession } from '@/api/gen'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { safeRedirect } from '@/lib/redirect'
import { loginMessage } from './login-message'

type FormState = { status: 'idle' | 'pending' } | { status: 'failed', message: string }

// the password goes from the field to the request and nowhere else: the input is uncontrolled and the sdk is called
// directly, since a mutation would keep its variables in the cache
export function LoginForm({ redirectTo }: { redirectTo?: string }) {
  const router = useRouter()
  const { api, queryClient } = useRouteContext({ from: '__root__' })
  const [state, setState] = useState<FormState>({ status: 'idle' })
  const inputRef = useRef<HTMLInputElement>(null)
  const id = useId()

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const password = new FormData(event.currentTarget).get('password')
    if (typeof password !== 'string' || password === '' || state.status === 'pending')
      return
    setState({ status: 'pending' })
    try {
      await createSession({
        client: api,
        body: { password },
        throwOnError: true,
      })
    }
    catch (error) {
      setState({
        status: 'failed',
        message: loginMessage(error),
      })
      inputRef.current?.focus()
      inputRef.current?.select()
      return
    }
    // nothing the signed-out page fetched carries over
    queryClient.clear()
    await router.navigate({
      href: safeRedirect(redirectTo),
    })
  }

  const failed = state.status === 'failed'
  return (
    <form method="post" action="/login" onSubmit={e => void submit(e)} className="flex flex-col gap-5 rounded-lg border bg-card p-6">
      {/* password managers file a password under a user name */}
      <input type="text" name="username" autoComplete="username" value="owner" readOnly hidden />
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-password`}>Password</Label>
        <Input
          ref={inputRef}
          id={`${id}-password`}
          name="password"
          type="password"
          autoComplete="current-password"
          required
          autoFocus
          aria-invalid={failed || undefined}
          aria-describedby={failed
            ? `${id}-error`
            : undefined}
        />
        {failed && <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{state.message}</p>}
      </div>
      <Button type="submit" size="lg" disabled={state.status === 'pending'}>
        {state.status === 'pending' && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
        Sign in
      </Button>
    </form>
  )
}
