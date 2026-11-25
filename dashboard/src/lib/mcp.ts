import type { PatchToolRequest } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { acknowledgeMcpTools, patchMcpTool, refreshMcpServer } from '@/api/gen'
import { listMcpServersOptions, listMcpToolsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export function mcpServersQueryOptions(api: Client) {
  return sharedKey(listMcpServersOptions({ client: api }))
}

export function mcpToolsQueryOptions(api: Client, server: string) {
  return sharedKey(listMcpToolsOptions({
    client: api,
    path: { name: server },
  }))
}

// every server's state and every server's tools; the agent topic's mcp events refresh them too
const MCP_KEYS = [
  [
    { _id: 'listMcpServers' },
  ],
  [
    { _id: 'listMcpTools' },
  ],
]

export function useMcpActions(server: string) {
  const api = useApi()
  const queryClient = useQueryClient()
  async function refreshing(action: () => Promise<unknown>) {
    try {
      await action()
    }
    finally {
      for (const queryKey of MCP_KEYS)
        void queryClient.invalidateQueries({ queryKey })
    }
  }
  return {
    // 202: the server lists the tools again in the background, and its outcome shows in the list
    refresh: () => refreshing(() => refreshMcpServer({
      client: api,
      path: { name: server },
      throwOnError: true,
    })),
    patch: (remote: string, body: PatchToolRequest) => refreshing(() => patchMcpTool({
      client: api,
      path: { name: server, remote },
      body,
      throwOnError: true,
    })),
    // the hashes the page showed, never all: a tool that changed again since is refused (409 hash_mismatch), not
    // approved unseen
    acknowledge: (hashes: Record<string, string>) => refreshing(() => acknowledgeMcpTools({
      client: api,
      path: { name: server },
      body: { hashes },
      throwOnError: true,
    })),
  }
}
