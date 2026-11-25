import type { ClaimView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { TvIcon } from 'lucide-react'
import { useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { useApi } from '@/api/use-api'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { buttonVariants } from '@/components/ui/button-variants'
import { claimCodeText, claimsQueryOptions, useClaimActions } from '@/lib/claims'
import { useBoardNames } from '../devices/use-boards'
import { ClaimCountdown } from './claim-countdown'

function DeclineButton({ claim, onFailed }: { claim: ClaimView, onFailed: (message: string) => void }) {
  const actions = useClaimActions()
  const [pending, setPending] = useState(false)
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      aria-label={`Decline ${claimCodeText(claim.code)}`}
      aria-disabled={pending || undefined}
      onClick={() => {
        if (pending)
          return
        setPending(true)
        actions.decline(claim.code).catch((failure: unknown) => {
          // one gone meanwhile (expired, approved elsewhere) is what the owner wanted
          if (!(failure instanceof ApiError && failure.status === 404))
            onFailed(describeError(failure))
        }).finally(() => setPending(false))
      }}
    >
      Decline
    </Button>
  )
}

// the screens that asked to join and wait for the owner's word: each one's code (as its screen shows it), its board,
// where it asked from and how long its code is good
export function ClaimsPage() {
  const api = useApi()
  const boards = useBoardNames()
  const list = useQuery(claimsQueryOptions(api))
  const [failure, setFailure] = useState<string>()
  const columns: Column<ClaimView>[] = [
    {
      id: 'code',
      header: 'Code',
      sortValue: c => c.code,
      cell: c => <span className="font-mono font-semibold tracking-wider">{claimCodeText(c.code)}</span>,
    },
    {
      id: 'board',
      header: 'Board',
      sortValue: c => boards.get(c.board) ?? c.board,
      cell: c => boards.get(c.board) ?? c.board,
    },
    {
      id: 'source',
      header: 'From',
      sortValue: c => c.source,
      cell: c => <span className="font-mono text-xs">{c.source}</span>,
      className: 'max-sm:hidden',
    },
    {
      id: 'expires',
      header: 'Code good for',
      sortValue: c => Date.parse(c.expiresAt),
      cell: c => <ClaimCountdown expiresAt={c.expiresAt} />,
    },
  ]
  return (
    <>
      <PageHeader
        title="Screens waiting"
        description="A screen joins by itself: it shows a code and a QR, and waits here until you approve it with a name and a room, or decline it."
      />
      {failure && <p role="alert" className="mb-3 text-sm text-destructive">{`The decline failed. ${failure}`}</p>}
      <DataTable
        caption="Screens waiting"
        columns={columns}
        rows={list.data}
        rowKey={c => c.code}
        rowActions={c => (
          <div className="flex justify-end gap-1">
            <Link
              to="/claims/$code"
              params={{ code: c.code }}
              aria-label={`Approve ${claimCodeText(c.code)}`}
              className={buttonVariants({ variant: 'outline', size: 'sm' })}
            >
              Approve
            </Link>
            <DeclineButton claim={c} onFailed={setFailure} />
          </div>
        )}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The waiting screens could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            icon={TvIcon}
            title="No screen is waiting"
            description="Start Cadence on a TV: it shows a code, which appears here within seconds."
          />
        )}
      />
    </>
  )
}
