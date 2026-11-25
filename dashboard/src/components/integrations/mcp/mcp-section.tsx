import { useQuery } from '@tanstack/react-query'
import { WrenchIcon } from 'lucide-react'
import { useApi } from '@/api/use-api'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { mcpServersQueryOptions } from '@/lib/mcp'
import { ProvidersLink, SettingLink } from '../setting-link'
import { McpServer } from './mcp-server'

// the servers the mcp setting lists, each with its tools; they start and stop with the setting, without a restart
export function McpSection() {
  const api = useApi()
  const servers = useQuery(mcpServersQueryOptions(api))
  if (servers.isPending)
    return <LoadingLines lines={3} label="Loading the MCP servers" />
  if (servers.isError) {
    return (
      <div className="rounded-lg border bg-card">
        <ErrorState title="The MCP servers could not be listed" error={servers.error} onRetry={() => void servers.refetch()} />
      </div>
    )
  }
  if (servers.data.length === 0) {
    return (
      <div className="rounded-lg border bg-card">
        <EmptyState
          icon={WrenchIcon}
          title="No MCP servers"
          description="Add an mcp-http provider with the server's URL, then list it in the mcp setting; its tools show here, each off until you offer it."
          action={(
            <div className="flex flex-wrap justify-center gap-2">
              <ProvidersLink />
              <SettingLink settingKey="mcp" appearance="button" />
            </div>
          )}
        />
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-10">
      {servers.data.map(server => <McpServer key={server.instance} server={server} />)}
    </div>
  )
}
