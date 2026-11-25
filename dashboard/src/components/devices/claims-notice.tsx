import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { TvIcon } from 'lucide-react'
import { useApi } from '@/api/use-api'
import { claimsQueryOptions } from '@/lib/claims'

// screens that asked to join and wait for the owner: one line above the list, to the page that approves them
export function ClaimsNotice() {
  const api = useApi()
  const claims = useQuery(claimsQueryOptions(api))
  const count = claims.data?.length ?? 0
  if (count === 0)
    return null
  return (
    <div role="status" className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border bg-info-surface px-4 py-3 text-sm">
      <TvIcon aria-hidden="true" className="size-4" />
      <span className="font-medium">
        {count === 1
          ? 'A screen waits to be approved.'
          : `${count} screens wait to be approved.`}
      </span>
      <Link to="/claims" className="underline underline-offset-4">Screens waiting</Link>
    </div>
  )
}
