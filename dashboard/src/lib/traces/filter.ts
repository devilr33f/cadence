import type { TraceRow } from './book'

export interface TraceFilter {
  liveOnly: boolean
  errorsOnly: boolean
  name: string
}

export const NO_TRACE_FILTER: TraceFilter = { liveOnly: false, errorsOnly: false, name: '' }

// what the page holds narrowed in place: running ones, failed ones, a name that holds the text (any case)
export function filterRows(rows: readonly TraceRow[], filter: TraceFilter): readonly TraceRow[] {
  const name = filter.name.trim().toLowerCase()
  if (!filter.liveOnly && !filter.errorsOnly && name === '')
    return rows
  return rows.filter(r => (!filter.liveOnly || r.live)
    && (!filter.errorsOnly || r.error)
    && (name === '' || r.name.toLowerCase().includes(name)))
}
