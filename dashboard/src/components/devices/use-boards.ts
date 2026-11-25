import type { BoardView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/use-api'
import { boardsQueryOptions } from '@/lib/devices'

const NONE: ReadonlyMap<string, string> = new Map()

function toNames(boards: BoardView[]): ReadonlyMap<string, string> {
  return new Map(boards.map(b => [b.id, b.name]))
}

// a board's name by its id (a device's model); a board the server no longer lists keeps its id
export function useBoardNames(): ReadonlyMap<string, string> {
  const api = useApi()
  return useQuery({
    ...boardsQueryOptions(api),
    // the boards are the server build's: they change only with it
    staleTime: Infinity,
    select: toNames,
  }).data ?? NONE
}
