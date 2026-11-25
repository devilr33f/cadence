export type SortDirection = 'asc' | 'desc'
export type SortValue = string | number | null | undefined

export interface SortState {
  column: string
  direction: SortDirection
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

const isEmpty = (value: SortValue) => value === null || value === undefined || value === ''

// empty values go last in both directions; equal values keep their order
export function sortRows<T>(rows: readonly T[], value: (row: T) => SortValue, direction: SortDirection): T[] {
  const sign = direction === 'asc'
    ? 1
    : -1
  return rows
    .map(row => ({
      row,
      key: value(row),
    }))
    .sort((a, b) => {
      if (isEmpty(a.key) || isEmpty(b.key))
        return Number(isEmpty(a.key)) - Number(isEmpty(b.key))
      const order = typeof a.key === 'number' && typeof b.key === 'number'
        ? a.key - b.key
        : collator.compare(String(a.key), String(b.key))
      return order * sign
    })
    .map(({ row }) => row)
}

export function nextSort(current: SortState | null, column: string, first: SortDirection): SortState {
  if (current?.column !== column)
    return { column, direction: first }
  return {
    column,
    direction: current.direction === 'asc'
      ? 'desc'
      : 'asc',
  }
}
