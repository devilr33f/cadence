import type { UseQueryResult } from '@tanstack/react-query'
import type { MemoryJson } from '@/api/gen'
import { useQueries } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useApi } from '@/api/use-api'
import { scopeMemoriesQueryOptions } from '@/lib/memory'
import { changeOf } from './memory-words'

function toTexts(results: UseQueryResult<MemoryJson[]>[]): ReadonlyMap<string, string> {
  return new Map(results.flatMap(result => (result.data ?? []).map(m => [m.id, m.text] as const)))
}

// the active text each update proposal would overwrite, by the id it replaces: the scopes the proposals touch are read
// whole, since no route reads one memory by id
export function useReplacedTexts(suggestions: readonly MemoryJson[] | undefined): ReadonlyMap<string, string> {
  const api = useApi()
  const scopes = useMemo(() => {
    const seen = new Map<string, { scope: string, deviceId: string | null }>()
    for (const m of suggestions ?? []) {
      if (changeOf(m) === 'update')
        seen.set(`${m.scope}/${m.deviceId ?? ''}`, { scope: m.scope, deviceId: m.deviceId })
    }
    return [
      ...seen.values(),
    ]
  }, [suggestions])
  return useQueries({
    queries: scopes.map(({ scope, deviceId }) => scopeMemoriesQueryOptions(api, scope, deviceId)),
    combine: toTexts,
  })
}
