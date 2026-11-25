import { useSearch } from '@tanstack/react-router'
import { ThemeToggle } from '@/components/shell/theme-toggle'
import { Wordmark } from '@/components/shell/wordmark'
import { LoginForm } from './login-form'

export function LoginPage() {
  const { redirect } = useSearch({ from: '/login' })
  return (
    <div className="flex min-h-svh flex-col">
      <header className="flex h-(--header-height) shrink-0 items-center justify-between px-4 md:px-8">
        <Wordmark />
        <ThemeToggle />
      </header>
      <main id="main" className="flex flex-1 justify-center px-4 pt-[12svh] pb-16">
        <div className="flex w-full max-w-sm flex-col gap-6">
          <div>
            <h1 className="text-xl font-semibold">Sign in</h1>
            <p className="mt-1 text-base text-muted-foreground">The owner's password opens the dashboard.</p>
          </div>
          <LoginForm redirectTo={redirect} />
        </div>
      </main>
    </div>
  )
}
