// @vitest-environment jsdom
import type { Column } from './data-table'
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { setupUser } from '@/test/typing'
import { DataTable } from './data-table'

interface Speaker {
  id: string
  name: string
  seen: number | null
}

const speakers: Speaker[] = [
  { id: 'a', name: 'kitchen', seen: 300 },
  { id: 'b', name: 'Bedroom', seen: null },
  { id: 'c', name: 'attic', seen: 100 },
]

const columns: Column<Speaker>[] = [
  {
    id: 'name',
    header: 'Name',
    cell: s => s.name,
    sortValue: s => s.name,
  },
  {
    id: 'seen',
    header: 'Last seen',
    cell: s => s.seen ?? 'never',
    sortValue: s => s.seen,
    firstSort: 'desc',
  },
]

afterEach(cleanup)

function names() {
  return screen.getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell')[0]!.textContent)
}

function header(name: string) {
  return screen.getByRole('columnheader', {
    name: new RegExp(name),
  })
}

describe('data table', () => {
  it('keeps the given order until a column is sorted', () => {
    render(<DataTable caption="Devices" columns={columns} rows={speakers} rowKey={s => s.id} empty="No devices" />)
    expect(names()).toEqual(['kitchen', 'Bedroom', 'attic'])
    expect(header('Name').getAttribute('aria-sort')).toBe('none')
  })

  it('sorts by a column, ignoring case, and reverses on a second click', async () => {
    const user = setupUser()
    render(<DataTable caption="Devices" columns={columns} rows={speakers} rowKey={s => s.id} empty="No devices" />)

    await user.click(screen.getByRole('button', { name: 'Name' }))
    expect(names()).toEqual(['attic', 'Bedroom', 'kitchen'])
    expect(header('Name').getAttribute('aria-sort')).toBe('ascending')

    await user.click(screen.getByRole('button', { name: 'Name' }))
    expect(names()).toEqual(['kitchen', 'Bedroom', 'attic'])
    expect(header('Name').getAttribute('aria-sort')).toBe('descending')
  })

  it('starts a column in its own first direction and puts empty values last either way', async () => {
    const user = setupUser()
    render(<DataTable caption="Devices" columns={columns} rows={speakers} rowKey={s => s.id} empty="No devices" />)

    await user.click(screen.getByRole('button', { name: 'Last seen' }))
    expect(names()).toEqual(['kitchen', 'attic', 'Bedroom'])
    expect(header('Last seen').getAttribute('aria-sort')).toBe('descending')

    await user.click(screen.getByRole('button', { name: 'Last seen' }))
    expect(names()).toEqual(['attic', 'kitchen', 'Bedroom'])
  })

  it('applies an initial sort', () => {
    render(
      <DataTable
        caption="Devices"
        columns={columns}
        rows={speakers}
        rowKey={s => s.id}
        empty="No devices"
        initialSort={{ column: 'name', direction: 'asc' }}
      />,
    )
    expect(names()).toEqual(['attic', 'Bedroom', 'kitchen'])
  })

  it('shows the empty state across the table when there are no rows', () => {
    render(<DataTable caption="Devices" columns={columns} rows={[]} rowKey={s => s.id} empty="No devices yet" />)
    const cell = screen.getByText('No devices yet').closest('td')!
    expect(cell.getAttribute('colspan')).toBe('2')
    expect(screen.getAllByRole('row')).toHaveLength(2)
  })

  it('marks itself busy while loading and shows no empty state', () => {
    render(<DataTable caption="Devices" columns={columns} rows={undefined} status="loading" rowKey={s => s.id} empty="No devices yet" />)
    expect(screen.getByRole('table', { name: 'Devices' }).getAttribute('aria-busy')).toBe('true')
    expect(screen.queryByText('No devices yet')).toBeNull()
  })

  it('shows the error in place of the rows', () => {
    render(
      <DataTable
        caption="Devices"
        columns={columns}
        rows={undefined}
        status="error"
        error="Could not load devices"
        rowKey={s => s.id}
        empty="No devices yet"
      />,
    )
    expect(screen.getByText('Could not load devices')).toBeTruthy()
    expect(screen.queryByText('No devices yet')).toBeNull()
  })

  it('gives each row its actions in a last column', () => {
    render(
      <DataTable
        caption="Devices"
        columns={columns}
        rows={speakers}
        rowKey={s => s.id}
        empty="No devices"
        rowActions={s => <button type="button">{`Open ${s.name}`}</button>}
      />,
    )
    expect(screen.getByRole('columnheader', { name: 'Actions' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Open attic' })).toBeTruthy()
  })
})
