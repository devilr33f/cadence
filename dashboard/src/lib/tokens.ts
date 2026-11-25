import type { QueryClient } from '@tanstack/react-query'
import type { CreatedToken, CreateTokenRequest } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { createToken, deleteToken, tokenTools } from '@/api/gen'
import { listMcpEndpointToolsOptions, listTokensOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export const TOKEN_SCOPES = [
  'admin',
  'ha',
  'metrics',
  'presence',
  'mcp',
] as const satisfies readonly CreateTokenRequest['scopes'][number][]

export type TokenScope = (typeof TOKEN_SCOPES)[number]

export function tokensQueryOptions(api: Client) {
  return sharedKey(listTokensOptions({ client: api }))
}

// the MCP endpoint's tools, the names an mcp token's list may name; the
// server is their only source, so the editor reads them fresh
export function mcpToolsQueryOptions(api: Client) {
  return sharedKey(listMcpEndpointToolsOptions({ client: api }))
}

// drops names the server no longer serves from a saved choice
export function servedTools(tools: { name: string }[] | undefined, value: readonly string[]): string[] {
  if (tools === undefined)
    return [...value]
  const known = new Set(tools.map(t => t.name))
  return value.filter(name => known.has(name))
}

// in the cache before the page renders; a failure is the page's to show (and retry), not the navigation's to wait for
export async function prefetchTokens(queryClient: QueryClient, api: Client) {
  await queryClient.prefetchQuery({
    ...tokensQueryOptions(api),
    retry: false,
  })
}

// called directly, not as mutations: the created token is answered once and must reach no cache, only the dialog that
// shows it. no live topic covers tokens, so each refreshes the list itself: a create after any answer, since one whose
// answer was lost may still have been made
export function useTokenActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  const refresh = () => queryClient.invalidateQueries({ queryKey: tokensQueryOptions(api).queryKey })
  return {
    async create(request: CreateTokenRequest): Promise<CreatedToken> {
      try {
        const { data } = await createToken({ client: api, body: request, throwOnError: true })
        return data
      }
      finally {
        void refresh()
      }
    },
    // the list is read again after the confirmation closed, so its row is not unmounted with the dialog still open
    async remove(name: string) {
      await deleteToken({
        client: api,
        path: { name },
        throwOnError: true,
      })
      void refresh()
    },
    // a token's tool list changes without touching the token itself
    async setTools(name: string, mcpTools: string[]) {
      await tokenTools({
        client: api,
        path: { name },
        body: { mcpTools },
        throwOnError: true,
      })
      void refresh()
    },
  }
}
