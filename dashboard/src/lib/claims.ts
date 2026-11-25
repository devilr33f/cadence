import type { QueryClient } from '@tanstack/react-query'
import type { ApproveOutBody } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { approveClaim, declineClaim } from '@/api/gen'
import { getClaimOptions, listClaimsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'
import { devicesQueryOptions } from './devices'

// a claim lives 10 minutes and no live topic covers claims: the list is read again on this beat while a page shows it
export const CLAIMS_REFRESH_MS = 3000

export function claimsQueryOptions(api: Client) {
  return {
    ...sharedKey(listClaimsOptions({ client: api })),
    refetchInterval: CLAIMS_REFRESH_MS,
  }
}

export function claimQueryOptions(api: Client, code: string) {
  return sharedKey(getClaimOptions({
    client: api,
    path: { code },
  }))
}

// a claim's code as the screen shows it: two groups of three ("K7M · 4QX")
export function claimCodeText(code: string): string {
  return code.length === 6
    ? `${code.slice(0, 3)} · ${code.slice(3)}`
    : code
}

// what the owner may type or the qr carries, in the server's form: upper case, no grouping
export function normalizeClaimCode(code: string): string {
  return code.toUpperCase().replace(/[\s·-]/g, '')
}

export async function prefetchClaims(queryClient: QueryClient, api: Client) {
  await queryClient.prefetchQuery({
    ...claimsQueryOptions(api),
    retry: false,
  })
}

// called directly, not as mutations; the claims and the devices are read again after any answer, since one whose answer
// was lost may still have landed
export function useClaimActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: claimsQueryOptions(api).queryKey })
    void queryClient.invalidateQueries({ queryKey: devicesQueryOptions(api).queryKey })
  }
  return {
    async approve(code: string, name: string, room: string): Promise<ApproveOutBody> {
      try {
        const { data } = await approveClaim({
          client: api,
          path: { code },
          body: { name, room },
          throwOnError: true,
        })
        return data
      }
      finally {
        refresh()
      }
    },
    async decline(code: string) {
      try {
        await declineClaim({
          client: api,
          path: { code },
          throwOnError: true,
        })
      }
      finally {
        refresh()
      }
    },
  }
}
