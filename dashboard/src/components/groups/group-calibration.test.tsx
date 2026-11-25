// @vitest-environment jsdom
import type { CalibrationMemberView, CalibrationView } from '@/api/gen'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextBoosts } from './calibration-retry'
import { BOOST_MAXED, BOOST_NOTE, GroupCalibration, NO_MIC_HINT } from './group-calibration'

afterEach(cleanup)

const names = new Map([
  ['a', 'Bedroom Station 2'],
  ['b', 'Bedroom TV'],
])

function member(deviceId: string, code: string, extra: Partial<CalibrationMemberView> = {}): CalibrationMemberView {
  return {
    deviceId,
    code,
    offsetUs: 0,
    confidence: 0.9,
    trimUs: 0,
    spreadUs: 0,
    ci95Us: 0,
    rounds: 0,
    micChannels: 0,
    ...extra,
  }
}

function run(mode: 'mics' | 'listener', codes: [string, string], extra: Partial<CalibrationView> = {}): CalibrationView {
  return {
    code: 'incomplete',
    mode,
    applied: false,
    residualUs: 0,
    members: [
      member('a', codes[0]),
      member('b', codes[1]),
    ],
    ...extra,
  }
}

function cells(row: number): string[] {
  const rows = within(screen.getByRole('table', { name: 'Calibration' })).getAllByRole('row')
  return within(rows[row]!).getAllByRole('cell').map(c => c.textContent ?? '')
}

// a tv stick has no microphone: speaker microphones can never measure its group, so the panel names the mode that can
describe('group calibration', () => {
  it('points a microphone-less group at the listener mode', () => {
    render(<GroupCalibration calibration={run('mics', ['isolated', 'no_capture'])} names={names} />)
    expect(screen.getByRole('status').textContent).toContain(NO_MIC_HINT)
    expect(screen.getByText('No other microphone heard it (isolated)')).toBeTruthy()
  })

  it('says nothing more when every member has a microphone', () => {
    render(<GroupCalibration calibration={run('mics', ['ok', 'weak'])} names={names} />)
    expect(screen.getByRole('status').textContent).not.toContain(NO_MIC_HINT)
  })

  it('shows each member\'s spread, the rounds it kept, its microphones, and a member whose rounds disagree', () => {
    render(
      <GroupCalibration
        calibration={{
          ...run('mics', ['ok', 'unstable'], { rounds: 4, code: 'incomplete' }),
          members: [
            member('a', 'ok', {
              offsetUs: -1500,
              spreadUs: 42,
              rounds: 4,
              micChannels: 4,
            }),
            member('b', 'unstable', { spreadUs: 820, rounds: 3, micChannels: 1 }),
          ],
        }}
        names={names}
      />,
    )
    expect(cells(1)).toEqual([
      'Bedroom Station 24 mics',
      'Measured',
      '-1.50 ms',
      '0.04 ms',
      '—',
      '4/4',
      '0.00 ms',
      '90%',
      '—',
    ])
    expect(cells(2)!.slice(0, 6)).toEqual([
      'Bedroom TV1 mic',
      'Rounds disagree (unstable)',
      '—',
      '0.82 ms',
      '—',
      '3/4',
    ])
  })

  it('names the output each member\'s trim applies to', () => {
    render(
      <GroupCalibration
        calibration={{
          ...run('mics', ['ok', 'ok'], { code: 'ok', applied: true }),
          members: [
            member('a', 'ok', {
              output: { id: 'speaker', kind: 'speaker' },
            }),
            member('b', 'ok', {
              output: { id: 'bt:aa:bb:cc:dd:ee:10', kind: 'bluetooth', delayHintUs: 145000 },
            }),
          ],
        }}
        names={names}
      />,
    )
    expect(cells(1)![0]).toContain('Speaker')
    expect(cells(2)![0]).toContain('Bluetooth · bt:aa:bb:cc:dd:ee:10')
  })

  it('shows a coded member\'s 95% range and explains a missed probe', () => {
    render(
      <GroupCalibration
        calibration={{
          ...run('mics', ['ok', 'missed'], { rounds: 4 }),
          members: [
            member('a', 'ok', {
              offsetUs: -1500,
              spreadUs: 42,
              ci95Us: 1250,
              rounds: 4,
            }),
            member('b', 'missed'),
          ],
        }}
        names={names}
      />,
    )
    expect(cells(1)).toContain('±1.25 ms')
    expect(cells(2)![1]).toBe('Not heard: its probe was not found (missed)')
  })

  it('shows the speakers\' own measure and each member\'s path beside a listener run', () => {
    render(
      <GroupCalibration
        calibration={{
          ...run('listener', ['ok', 'ok'], {
            code: 'ok',
            applied: true,
            rounds: 4,
            micsResidualUs: 30,
          }),
          members: [
            member('a', 'ok', { micsCode: 'ok', micsOffsetUs: -1000, pathUs: -2915 }),
            member('b', 'ok', { micsCode: 'no_capture' }),
          ],
        }}
        names={names}
      />,
    )
    const headers = within(screen.getByRole('table', { name: 'Calibration' })).getAllByRole('columnheader').map(h => h.textContent)
    expect(headers).toContain('Speaker mics')
    expect(headers).toContain('Path to you')
    expect(cells(1)!.slice(-2)).toEqual(['-1.00 ms', '-2.92 ms · ≈-1.00 m'])
    expect(cells(2)!.slice(-2)).toEqual(['No microphone', '—'])
  })

  it('says why a device it did not hear declined to play', () => {
    render(
      <GroupCalibration
        calibration={{
          ...run('listener', ['ok', 'missed'], { rounds: 4 }),
          members: [
            member('a', 'ok', { micsCode: 'ok' }),
            member('b', 'missed', { micsCode: 'epoch' }),
          ],
        }}
        names={names}
      />,
    )
    expect(cells(2)![1]).toBe('Did not play: Playing other audio (epoch)')
  })

  it('shows the ultrasonic capability round beside each audible result', () => {
    render(
      <GroupCalibration
        calibration={{
          ...run('mics', ['ok', 'ok'], { code: 'ok', applied: true }),
          members: [
            member('a', 'ok', { ultrasonic: 'ok' }),
            member('b', 'ok', { ultrasonic: 'unsupported' }),
          ],
        }}
        names={names}
      />,
    )
    const table = screen.getByRole('table', { name: 'Calibration' })
    expect(within(table).getAllByRole('columnheader').map(h => h.textContent)).toContain('Ultrasonic')
    expect(cells(1)).toContain('Reached (ok)')
    expect(cells(2)).toContain('Unsupported')
  })
})

describe('retry louder', () => {
  it('boosts only the faint members by 6 dB, up to 14, and stops at the ceiling', () => {
    const c = {
      ...run('mics', ['ok', 'weak']),
      members: [
        member('a', 'ok', { confidence: 0.3 }),
        member('b', 'weak'),
        {
          ...member('c', 'ok'),
          confidence: 0.95,
        },
      ],
    }
    expect(nextBoosts(c, {})).toEqual({ a: 6, b: 6 })
    expect(nextBoosts(c, { a: 12, b: 6, c: 6 })).toEqual({ a: 14, b: 12, c: 6 })
    expect(nextBoosts(c, { a: 14, b: 14 })).toBeUndefined()
    expect(nextBoosts(run('mics', ['ok', 'ok']), {})).toBeUndefined()
  })

  it('reruns with the louder boosts from its press, and is shown only when some member is faint', async () => {
    const onRetry = vi.fn(async () => {})
    const { rerender } = render(<GroupCalibration calibration={run('listener', ['ok', 'missed'])} names={names} boosts={{ a: 6 }} onRetry={onRetry} />)
    expect(screen.getByText(`${BOOST_NOTE} Bedroom TV.`)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry louder' }))
    // inside the press: a listener run's microphone must start in it
    expect(onRetry).toHaveBeenCalledWith({ a: 6, b: 6 }, expect.any(Function))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry louder' }).getAttribute('aria-disabled')).toBeNull())

    rerender(<GroupCalibration calibration={run('listener', ['ok', 'missed'])} names={names} boosts={{ b: 14 }} onRetry={onRetry} />)
    expect(screen.getByRole('button', { name: 'Retry louder' }).getAttribute('aria-disabled')).toBe('true')
    expect(screen.getByText(BOOST_MAXED)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Retry louder' }))
    expect(onRetry).toHaveBeenCalledOnce()

    rerender(<GroupCalibration calibration={run('listener', ['ok', 'ok'])} names={names} onRetry={onRetry} />)
    expect(screen.queryByRole('button', { name: 'Retry louder' })).toBeNull()
  })

  it('says why a retry failed', async () => {
    const onRetry = vi.fn(async () => {
      throw new DOMException('denied', 'NotAllowedError')
    })
    render(<GroupCalibration calibration={run('listener', ['ok', 'weak'])} names={names} onRetry={onRetry} />)
    fireEvent.click(screen.getByRole('button', { name: 'Retry louder' }))
    expect((await screen.findByRole('alert')).textContent).toContain('microphone was not allowed')
  })
})
