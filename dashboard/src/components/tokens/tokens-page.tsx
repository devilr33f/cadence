import type { TokenView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { KeyRoundIcon, PlusIcon } from 'lucide-react'
import { Fragment, useMemo, useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { useTimeZone } from '@/lib/time-zone'
import { tokensQueryOptions, useTokenActions } from '@/lib/tokens'
import { CreateTokenDialog } from './create-token-dialog'
import { EditTokenToolsDialog } from './edit-token-tools-dialog'
import { tokenColumns } from './token-table'

const BY_NAME = { column: 'name', direction: 'asc' } as const

export function TokensPage() {
  const api = useApi()
  const timeZone = useTimeZone()
  const list = useQuery(tokensQueryOptions(api))
  const actions = useTokenActions()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<TokenView | null>(null)
  // the page's action takes the focus back from every dialog: a row's revoke button, or the empty state's create, is
  // gone by the time it closes
  const createRef = useRef<HTMLButtonElement>(null)
  const columns = useMemo(() => tokenColumns(timeZone), [timeZone])

  const create = (size: 'default' | 'lg') => (
    <Button
      ref={size === 'lg'
        ? createRef
        : undefined}
      size={size}
      onClick={() => setCreating(true)}
    >
      <PlusIcon aria-hidden="true" />
      Create a token
    </Button>
  )

  return (
    <>
      <PageHeader
        title="API tokens"
        description="Bearer tokens for Home Assistant, scripts and metrics scrapers, each with the scopes it may use. A token is shown once, when it is made; revoke one that may have leaked."
        actions={create('lg')}
      />
      <DataTable
        caption="API tokens"
        columns={columns}
        rows={list.data}
        rowKey={t => t.name}
        initialSort={BY_NAME}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The tokens could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            icon={KeyRoundIcon}
            title="No API tokens"
            description="Home Assistant and scripts call the API with a token; the dashboard itself needs none."
            action={create('default')}
          />
        )}
        rowActions={token => (
          <Fragment key={token.name}>
            {token.scopes.includes('mcp') && (
              <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(token)}>Tools</Button>
            )}
            <ConfirmDialog
              trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Revoke ${token.name}`}>Revoke</Button>}
              title={`Revoke ${token.name}?`}
              description="Every request with it is refused from now on, and a live feed it opened closes within 20 seconds."
              confirmLabel="Revoke the token"
              tone="destructive"
              inlineError
              finalFocus={createRef}
              onConfirm={() => actions.remove(token.name)}
            />
          </Fragment>
        )}
      />
      <CreateTokenDialog open={creating} onOpenChange={setCreating} finalFocus={createRef} onCreate={actions.create} />
      {editing && (
        <EditTokenToolsDialog
          token={editing}
          open={editing !== null}
          onOpenChange={(next) => {
            if (!next)
              setEditing(null)
          }}
          onSave={actions.setTools}
          finalFocus={createRef}
        />
      )}
    </>
  )
}
