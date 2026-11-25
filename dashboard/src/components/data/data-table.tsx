import type { ReactNode } from 'react'
import type { SortDirection, SortState, SortValue } from './sort-rows'
import { cn } from 'cn'
import { ArrowDownIcon, ArrowUpIcon, ChevronsUpDownIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { nextSort, sortRows } from './sort-rows'

export interface Column<T> {
  id: string
  header: string
  cell: (row: T) => ReactNode
  // a column with a sort value gets a sortable header
  sortValue?: (row: T) => SortValue
  firstSort?: SortDirection
  align?: 'start' | 'end'
  className?: string
}

export interface DataTableProps<T> {
  caption: string
  columns: readonly Column<T>[]
  rows: readonly T[] | undefined
  rowKey: (row: T) => string
  status?: 'ready' | 'loading' | 'error'
  empty: ReactNode
  error?: ReactNode
  rowActions?: (row: T) => ReactNode
  initialSort?: SortState
  // a max height makes the container scroll and the header stick
  className?: string
}

const ARIA_SORT = { asc: 'ascending', desc: 'descending' } as const
const SKELETON_ROWS = ['a', 'b', 'c', 'd']

export function DataTable<T>({
  caption,
  columns,
  rows,
  rowKey,
  status = 'ready',
  empty,
  error,
  rowActions,
  initialSort,
  className,
}: DataTableProps<T>) {
  const [sort, setSort] = useState<SortState | null>(initialSort ?? null)

  const sorted = useMemo(() => {
    const column = sort && columns.find(c => c.id === sort.column)
    return rows && sort && column?.sortValue
      ? sortRows(rows, column.sortValue, sort.direction)
      : rows
  }, [rows, sort, columns])

  const span = columns.length + (rowActions ? 1 : 0)
  const whole = (content: ReactNode) => (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={span} className="h-auto p-0 whitespace-normal">{content}</TableCell>
    </TableRow>
  )

  let body: ReactNode
  if (status === 'loading') {
    body = SKELETON_ROWS.map(key => (
      <TableRow key={key} className="hover:bg-transparent">
        {Array.from({ length: span }, (_, i) => (
          <TableCell key={columns[i]?.id ?? 'actions'}>
            <Skeleton className={cn('h-3.5', i === 0
              ? 'w-32'
              : 'w-20')}
            />
          </TableCell>
        ))}
      </TableRow>
    ))
  }
  else if (status === 'error') {
    body = whole(error)
  }
  else if (!sorted?.length) {
    body = whole(empty)
  }
  else {
    body = sorted.map(row => (
      <TableRow key={rowKey(row)}>
        {columns.map(column => (
          <TableCell key={column.id} className={cn(column.align === 'end' && 'text-right', column.className)}>
            {column.cell(row)}
          </TableCell>
        ))}
        {rowActions && <TableCell className="w-0 py-0 pr-1.5 text-right">{rowActions(row)}</TableCell>}
      </TableRow>
    ))
  }

  return (
    <Table aria-busy={status === 'loading' || undefined} containerClassName={className}>
      <caption className="sr-only">{caption}</caption>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          {columns.map((column) => {
            const active = sort?.column === column.id
              ? sort.direction
              : undefined
            return (
              <TableHead
                key={column.id}
                aria-sort={column.sortValue
                  ? (active ? ARIA_SORT[active] : 'none')
                  : undefined}
                className={cn(column.align === 'end' && 'text-right', column.className)}
              >
                {column.sortValue
                  ? (
                      <SortButton
                        label={column.header}
                        direction={active}
                        onClick={() => setSort(current => nextSort(current, column.id, column.firstSort ?? 'asc'))}
                      />
                    )
                  : column.header}
              </TableHead>
            )
          })}
          {rowActions && <TableHead className="w-0"><span className="sr-only">Actions</span></TableHead>}
        </TableRow>
      </TableHeader>
      <TableBody>{body}</TableBody>
    </Table>
  )
}

function SortButton({ label, direction, onClick }: { label: string, direction?: SortDirection, onClick: () => void }) {
  const Icon = direction === 'asc'
    ? ArrowUpIcon
    : direction === 'desc'
      ? ArrowDownIcon
      : ChevronsUpDownIcon
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        '-mx-1.5 inline-flex h-7 items-center gap-1 rounded-sm px-1.5 transition-colors duration-150 hover:bg-background/60 hover:text-foreground',
        direction && 'text-foreground',
      )}
    >
      {label}
      <Icon aria-hidden="true" className={cn('size-3.5', !direction && 'opacity-50')} />
    </button>
  )
}
