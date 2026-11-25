// @vitest-environment jsdom

import type { ReactElement } from 'react'
import type { Column } from './data-table'
import { cleanup, render, screen } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, describe, expect, it, onTestFinished } from 'vitest'
import { stubLogLayout } from '@/test/logs'
import { ApplyBadge } from './apply-badge'
import { DataTable } from './data-table'
import { DeviceStatus } from './device-status'
import { LogView } from './log-view'
import { SeverityLabel } from './severity-label'
import { EmptyState, ErrorState } from './states'
import { Timeline } from './timeline'

afterEach(cleanup)

// jsdom has no layout, so colour contrast is left to tokens.test.ts; a component alone sits in no landmark
async function violations(ui: ReactElement) {
  const { container } = render(<main>{ui}</main>)
  const results = await axe.run(container, {
    rules: {
      'color-contrast': { enabled: false },
    },
  })
  return results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)
}

interface Row {
  id: string
  name: string
}

const columns: Column<Row>[] = [
  {
    id: 'name',
    header: 'Name',
    cell: r => r.name,
    sortValue: r => r.name,
  },
  {
    id: 'id',
    header: 'Id',
    cell: r => <code>{r.id}</code>,
  },
]

describe('no accessibility violations axe can see without layout', () => {
  it('in a data table with rows and row actions', async () => {
    expect(await violations(
      <DataTable
        caption="Devices"
        columns={columns}
        rows={[
          { id: 'a1', name: 'Kitchen' },
          { id: 'b2', name: 'Bedroom' },
        ]}
        rowKey={r => r.id}
        empty={null}
        rowActions={r => <button type="button">{`Open ${r.name}`}</button>}
        initialSort={{ column: 'name', direction: 'asc' }}
      />,
    )).toEqual([])
  })

  it('in a data table while empty, loading and failed', async () => {
    expect(await violations(
      <>
        <DataTable caption="Empty" columns={columns} rows={[]} rowKey={r => r.id} empty={<EmptyState title="No speakers yet" />} />
        <DataTable caption="Loading" columns={columns} rows={undefined} status="loading" rowKey={r => r.id} empty={null} />
        <DataTable
          caption="Failed"
          columns={columns}
          rows={undefined}
          status="error"
          rowKey={r => r.id}
          empty={null}
          error={<ErrorState title="Couldn't load the devices" error={new TypeError('fetch failed')} onRetry={() => {}} />}
        />
      </>,
    )).toEqual([])
  })

  it('in a live timeline with an expandable stop', async () => {
    expect(await violations(
      <Timeline
        label="Kitchen activity"
        live
        timeZone="UTC"
        entries={[
          {
            id: 't1',
            at: '2026-11-12T15:04:12Z',
            kind: 'turn',
            text: 'Woken by voice',
            detail: <p>Mode fast</p>,
          },
          {
            id: 't2',
            at: '2026-11-12T15:04:13Z',
            kind: 'tool',
            text: 'Weather for Bishkek',
          },
          {
            id: 't3',
            at: '2026-11-12T14:41:37Z',
            kind: 'error',
            text: 'alarms gen 7 failed',
          },
        ]}
      />,
    )).toEqual([])
  })

  it('in a log tail', async () => {
    // jsdom lays nothing out: the box gets a height, so its lines are in the page
    const restore = stubLogLayout()
    onTestFinished(restore)
    expect(await violations(
      <LogView
        label="Kitchen log"
        timeZone="UTC"
        records={[
          {
            id: 'l1',
            at: '2026-11-12T15:04:14.212Z',
            severity: 'info',
            source: 'cadenced',
            message: 'session_end',
            device: 'Kitchen',
            attrs: { ms: 6240 },
          },
          {
            kind: 'mark',
            id: 'm1',
            text: 'Lines may be missing here: the live feed reconnected.',
          },
          {
            id: 'l2',
            at: '2026-11-12T14:41:37.517Z',
            severity: 'error',
            source: 'cadenced',
            message: 'config_apply_failed',
          },
        ]}
      />,
    )).toEqual([])
    expect(screen.getAllByRole('listitem')).toHaveLength(3)
  })

  it('in the status badges', async () => {
    expect(await violations(
      <ul>
        <li><DeviceStatus status="online" /></li>
        <li><DeviceStatus status="enrolling" appearance="pill" /></li>
        <li><ApplyBadge desiredGen={15} appliedGen={14} appliedError="" /></li>
        <li><ApplyBadge desiredGen={7} appliedGen={7} appliedError="time_zone_unset" /></li>
        <li><SeverityLabel severity="warn" /></li>
      </ul>,
    )).toEqual([])
  })
})
