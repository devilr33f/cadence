import type { RefObject } from 'react'
import type { ToolView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { useQuery } from '@tanstack/react-query'
import { WrenchIcon } from 'lucide-react'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { DataTable } from '@/components/data/data-table'
import { EmptyState, ErrorState } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { mcpToolsQueryOptions, useMcpActions } from '@/lib/mcp'
import { ToolAccessDialog } from './tool-access-dialog'

// a description is what the model reads of the server, cleaned of control characters but untrusted: text, never markup
function Description({ text }: { text: string }) {
  return <span className="block max-w-[60ch] min-w-56 break-words whitespace-pre-wrap">{text}</span>
}

function accessText(tool: ToolView): string {
  return [
    tool.effects === 'read'
      ? 'Read'
      : 'Write',
    tool.trust === 'trusted'
      ? 'trusted'
      : 'untrusted',
    ...(tool.anyTurn
      ? ['any turn']
      : []),
  ].join(' · ')
}

// a changed tool is withdrawn until acknowledged; a skipped one is not offered, for the reason the server names
function ToolState({ tool }: { tool: ToolView }) {
  if (tool.changed)
    return <StatusBadge tone="warning" label="Needs acknowledgement" />
  if (tool.skip)
    return <StatusBadge tone="neutral" label="Not offered" detail={<span className="font-mono text-xs">{tool.skip}</span>} />
  return <StatusBadge tone="success" label="Available" />
}

const COLUMNS: Column<ToolView>[] = [
  {
    id: 'local',
    header: 'Tool',
    sortValue: t => t.local || null,
    cell: t => (
      <div className="flex flex-col py-1.5">
        <span className="font-mono text-xs">{t.local}</span>
        <span className="font-mono text-xs text-muted-foreground">{t.remote}</span>
      </div>
    ),
  },
  {
    id: 'description',
    header: 'Description',
    cell: t => <Description text={t.description} />,
    className: 'whitespace-normal py-2',
  },
  {
    id: 'access',
    header: 'Access',
    sortValue: accessText,
    cell: accessText,
  },
  {
    id: 'default',
    header: 'By default',
    sortValue: t => t.enabledDefault ? 1 : 0,
    cell: t => t.enabledDefault ? 'On' : 'Off',
    className: 'max-md:hidden',
  },
  {
    id: 'state',
    header: 'State',
    sortValue: t => t.changed
      ? 0
      : t.skip ? 1 : 2,
    cell: t => <ToolState tool={t} />,
  },
]

// acknowledging takes the hash the page showed: one that changed again since is refused, not approved unseen. its button
// goes with the change, so the focus then waits on the server's heading
function Acknowledge({ server, tool, heading }: { server: string, tool: ToolView, heading: RefObject<HTMLElement | null> }) {
  const actions = useMcpActions(server)
  return (
    <ConfirmDialog
      trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Acknowledge: ${tool.local}`}>Acknowledge</Button>}
      title={`Acknowledge ${tool.local}?`}
      description={(
        <span className="flex flex-col gap-2">
          <span>Its description, input schema or annotations changed, so it was withdrawn. This is what the model will read:</span>
          <span className="block rounded-md border bg-muted/40 p-3 break-words whitespace-pre-wrap text-foreground">{tool.description}</span>
        </span>
      )}
      confirmLabel="Acknowledge the tool"
      inlineError
      finalFocus={heading}
      // the hash the dialog opened on, with the description it showed; a newer one blocks the confirm until reopened
      version={tool.hash}
      onConfirm={opened => actions.acknowledge({ [tool.remote]: opened ?? tool.hash })}
    />
  )
}

export function McpTools({ server, heading }: { server: string, heading: RefObject<HTMLElement | null> }) {
  const api = useApi()
  const tools = useQuery(mcpToolsQueryOptions(api, server))
  const actions = useMcpActions(server)
  return (
    <DataTable
      caption={`Tools of ${server}`}
      columns={COLUMNS}
      rows={tools.data}
      rowKey={t => t.remote}
      status={tools.isError
        ? 'error'
        : tools.isPending ? 'loading' : 'ready'}
      error={<ErrorState title="The tools could not be listed" error={tools.error} onRetry={() => void tools.refetch()} />}
      empty={<EmptyState icon={WrenchIcon} title="No tools listed" description="The server has listed no tools yet; refresh it once it is reachable." />}
      rowActions={tool => (
        <div className="flex justify-end gap-1">
          {tool.changed && <Acknowledge server={server} tool={tool} heading={heading} />}
          {tool.local !== '' && <ToolAccessDialog tool={tool} onSave={patch => actions.patch(tool.remote, patch)} />}
        </div>
      )}
    />
  )
}
