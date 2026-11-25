// @vitest-environment jsdom
import type { GroupView } from '@/api/gen'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { GroupTracking } from './group-tracking'

const GROUP: GroupView = {
  id: 'g1',
  name: 'Upstairs',
  kind: 'group',
  active: true,
  leaderId: 'a',
  clockLeaderId: 'a',
  preferredLeader: '',
  latencyMs: 1000,
  bitrateKbps: 256,
  waitMs: 3000,
  pcm: false,
  autoLatency: true,
  autoBitrate: true,
  autoWait: true,
  followVideo: true,
  trackingEnabled: false,
  tracking: { state: 'off', members: [] },
  createdAt: '2026-11-19T10:00:00Z',
  updatedAt: '2026-11-19T10:00:00Z',
  members: [
    {
      deviceId: 'a',
      channel: 'both',
      trimMs: 0,
      trimUs: 0,
      online: true,
      status: {
        state: 'playing',
        output: { id: 'speaker', kind: 'speaker' },
        synced: true,
        clockErrorUs: 0,
        queueMs: 0,
        rejoins: 0,
        latencyMs: 1000,
        placementErrorUs: 0,
        steerPpm: 0,
        underruns: 0,
        tracking: { state: 'tracking', members: [] },
      },
      trims: [
        { outputId: 'speaker', trimUs: 0, ultrasonic: 'ok' },
      ],
    },
    {
      deviceId: 'b',
      channel: 'both',
      trimMs: 0,
      trimUs: 0,
      online: true,
      status: {
        state: 'playing',
        output: { id: 'hdmi', kind: 'hdmi' },
        synced: true,
        clockErrorUs: 0,
        queueMs: 0,
        rejoins: 0,
        latencyMs: 1000,
        placementErrorUs: 0,
        steerPpm: 0,
        underruns: 0,
        tracking: { state: 'off', members: [] },
      },
      trims: [
        { outputId: 'hdmi', trimUs: 0, ultrasonic: 'weak' },
      ],
    },
  ],
}

const NAMES = new Map([
  ['a', 'Bedroom'],
  ['b', 'TV'],
])

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('live tracking', () => {
  it('opts in through the whole-group update, off by default', () => {
    const onSave = vi.fn(async () => GROUP)
    render(<GroupTracking group={GROUP} names={NAMES} onSave={onSave} />)
    const toggle = screen.getByRole('switch', { name: 'Live tracking (ultrasonic)' })
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    expect(screen.getByText(/inaudible 18–20 kHz/)).toBeTruthy()
    expect(screen.getByText(/Bluetooth outputs cannot carry it/)).toBeTruthy()
    expect(screen.queryByText('Microphones listening')).toBeNull()
    fireEvent.click(toggle)
    expect(onSave).toHaveBeenCalledWith('g1', expect.objectContaining({
      trackingEnabled: true,
      name: 'Upstairs',
      members: [
        { deviceId: 'a', channel: 'both', trimMs: 0 },
        { deviceId: 'b', channel: 'both', trimMs: 0 },
      ],
    }))
  })

  it('shows microphone use only while tracking, with each member\'s ultrasonic and timing results', () => {
    vi.useFakeTimers({
      now: new Date('2026-11-19T10:00:30Z'),
    })
    const tracking: GroupView = {
      ...GROUP,
      trackingEnabled: true,
      tracking: {
        state: 'tracking',
        members: [
          {
            deviceId: 'a',
            offsetUs: 1926,
            ci95Us: 40,
            cycles: 6,
            lastCorrectionUs: -1200,
            lastCorrectionAt: '2026-11-19T10:00:00Z',
          },
        ],
      },
    }
    const { rerender } = render(<GroupTracking group={tracking} names={NAMES} onSave={vi.fn()} />)
    expect(screen.getByText('Microphones listening')).toBeTruthy()
    const table = screen.getByRole('table', { name: 'Live tracking members' })
    const rows = within(table).getAllByRole('row')
    expect(within(rows[1]!).getAllByRole('cell').map(c => c.textContent)).toEqual([
      'Bedroom',
      'Reached (ok)',
      '+1.93 ms ± 0.04 ms',
      '-1.20 ms · 30 s ago',
    ])
    expect(within(rows[2]!).getAllByRole('cell').map(c => c.textContent)).toEqual([
      'TV',
      'Weak (weak)',
      '—',
      '—',
    ])

    rerender(
      <GroupTracking
        group={{
          ...tracking,
          tracking: { state: 'idle', members: [] },
        }}
        names={NAMES}
        onSave={vi.fn()}
      />,
    )
    expect(screen.queryByText('Microphones listening')).toBeNull()
  })
})
