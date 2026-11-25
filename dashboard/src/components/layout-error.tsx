import { useRouter } from '@tanstack/react-router'
import { ErrorState } from '@/components/data/states'

// the session check failed (the server down or unreachable), or a page under the layout threw: say so in codes and
// offer another try
export function LayoutError({ error }: { error: unknown }) {
  const router = useRouter()
  return (
    <main id="main" className="mx-auto w-full max-w-xl px-4 py-16">
      <div className="rounded-lg border bg-card">
        <ErrorState title="The page could not be shown" error={error} onRetry={() => void router.invalidate()} />
      </div>
    </main>
  )
}
