import type { GoldProbeShape } from './calibration/probe'
import type { ChirpShape } from './chirp-detector'
import type { ListenerCalibrationView } from './listener-calibration'
import type { HeardMemberRequest } from '@/api/gen'
import { describe, expect, it, vi } from 'vitest'
import { DEFAULT_GOLD_PROBE, probeAt, probeDuration } from './calibration/probe'
import { CHIRP, chirpAt, LEGACY_CHIRP } from './chirp-detector'
import { runListenerCalibration, scheduleChirp } from './listener-calibration'

const RATE = 48000

// a run against a fake leader: the device makes its answer at 100 ms and it arrives at 200 ms; its chirps sound
// startsInMs after it was made, at each slot's offset plus the member's lateness
function fakeRun(scheduled: ListenerCalibrationView, shape: ChirpShape | GoldProbeShape, lateMs: Record<string, number>) {
  let clock = 0
  const order: string[] = []
  const stop = vi.fn(() => {
    const out = new Float32Array(Math.ceil(clock / 1000 * RATE))
    const durationUs = 'kind' in shape
      ? probeDuration(shape) * 1e6
      : shape.durationUs
    const length = Math.ceil(durationUs / 1e6 * RATE) + 2
    for (const s of scheduled.slots ?? []) {
      const at = (100 + (scheduled.startsInMs ?? 0) + s.offsetMs + lateMs[s.deviceId]!) / 1000 * RATE
      for (let i = Math.floor(at); i < Math.min(out.length, Math.floor(at) + length); i++) {
        out[i]! += 0.3 * ('kind' in shape
          ? probeAt(shape, s.code ?? 0, (i - at) / RATE)
          : chirpAt((i - at) / RATE, shape))
      }
    }
    return out
  })
  const applied: HeardMemberRequest[][] = []
  const run = runListenerCalibration({
    record: async () => {
      order.push('record')
      return {
        rate: RATE,
        frameNow: () => clock / 1000 * RATE,
        stop,
      }
    },
    calibrate: async () => {
      order.push('calibrate')
      clock = 200
      return scheduled
    },
    apply: async (id, members) => {
      order.push(`apply ${id}`)
      applied.push(members)
      return {
        code: 'ok',
        mode: 'listener',
        applied: true,
        residualUs: 0,
        members: [],
      }
    },
    now: () => clock,
    sleep: async (ms) => {
      clock += ms
    },
  })
  return {
    run,
    order,
    stop,
    applied,
    clock: () => clock,
  }
}

function scheduledView(slots: ListenerCalibrationView['slots'], extra: Partial<ListenerCalibrationView> = {}): ListenerCalibrationView {
  return {
    code: 'scheduled',
    mode: 'listener',
    applied: false,
    residualUs: 0,
    members: [],
    calibrationId: 'c1',
    startsInMs: 1500,
    slots,
    ...extra,
  }
}

describe('runListenerCalibration', () => {
  it('records before asking, finds every round where the schedule puts it, posts the arrivals and stops', async () => {
    const slots = [
      {
        deviceId: 'd1',
        offsetMs: 0,
        round: 0,
        code: 0,
      },
      {
        deviceId: 'd2',
        offsetMs: 1200,
        round: 0,
        code: 1,
      },
      {
        deviceId: 'd1',
        offsetMs: 2400,
        round: 1,
        code: 0,
      },
      {
        deviceId: 'd2',
        offsetMs: 3600,
        round: 1,
        code: 1,
      },
    ]
    const f = fakeRun(scheduledView(slots, {
      rounds: 2,
      chirp: { ...CHIRP },
    }), CHIRP, { d1: 0, d2: 21 })
    const result = await f.run
    expect(f.order).toEqual(['record', 'calibrate', 'apply c1'])
    expect(result.code).toBe('ok')
    // recorded until a second past the last slot's chirp
    expect(f.clock()).toBeGreaterThanOrEqual(100 + 1500 + 3600 + 250 + 1000 - 1)
    const members = f.applied[0]!
    expect(members.map(m => [m.deviceId, m.code, m.rounds])).toEqual([
      ['d1', 'ok', 2],
      ['d2', 'ok', 2],
    ])
    expect(Math.abs(members[0]!.arrivalUs)).toBeLessThan(25)
    expect(Math.abs(members[1]!.arrivalUs - 1_221_000)).toBeLessThan(25)
    expect(members[1]!.spreadUs).toBeLessThan(25)
    expect(f.stop).toHaveBeenCalled()
  })

  it('uses the schedule\'s gold codes and keeps the same posted arrival shape', async () => {
    const slots = [
      {
        deviceId: 'd1',
        offsetMs: 0,
        round: 0,
        code: 0,
      },
      {
        deviceId: 'd2',
        offsetMs: 1200,
        round: 0,
        code: 1,
      },
      {
        deviceId: 'd1',
        offsetMs: 2400,
        round: 1,
        code: 0,
      },
      {
        deviceId: 'd2',
        offsetMs: 3600,
        round: 1,
        code: 1,
      },
    ]
    const probe = { ...DEFAULT_GOLD_PROBE }
    const f = fakeRun(scheduledView(slots, { rounds: 2, probe }), probe, { d1: 0, d2: 21 })
    await f.run
    const members = f.applied[0]!
    expect(members.map(m => [m.deviceId, m.code, m.rounds])).toEqual([
      ['d1', 'ok', 2],
      ['d2', 'ok', 2],
    ])
    expect(Math.abs(members[0]!.arrivalUs)).toBeLessThan(50)
    expect(Math.abs(members[1]!.arrivalUs - 1_221_000)).toBeLessThan(50)
    expect(f.stop).toHaveBeenCalled()
  })

  it('hears a leader before rounds: one round of the legacy sweep', async () => {
    const slots = [
      {
        deviceId: 'd1',
        offsetMs: 0,
        round: 0,
        code: 0,
      },
      {
        deviceId: 'd2',
        offsetMs: 1500,
        round: 0,
        code: 1,
      },
    ]
    const f = fakeRun(scheduledView(slots), LEGACY_CHIRP, { d1: 0, d2: 21 })
    await f.run
    const members = f.applied[0]!
    expect(members.map(m => [m.code, m.rounds])).toEqual([
      ['ok', 1],
      ['ok', 1],
    ])
    expect(Math.abs(members[1]!.arrivalUs - 1_521_000)).toBeLessThan(25)
  })

  it('takes the schedule\'s chirp, else the legacy one', () => {
    expect(scheduleChirp(scheduledView([]))).toEqual(LEGACY_CHIRP)
    expect(scheduleChirp(scheduledView([], { rounds: 4 }))).toEqual(CHIRP)
    const own = {
      f0Hz: 500,
      f1Hz: 8000,
      durationUs: 200_000,
      fadeUs: 8_000,
    }
    expect(scheduleChirp(scheduledView([], { rounds: 4, chirp: own }))).toEqual(own)
  })

  it('stops the microphone when the leader refuses', async () => {
    const stop = vi.fn(() => new Float32Array(0))
    await expect(runListenerCalibration({
      record: async () => ({
        rate: RATE,
        frameNow: () => 0,
        stop,
      }),
      calibrate: async () => {
        throw new Error('api 409 playing')
      },
      apply: async () => {
        throw new Error('unreached')
      },
      now: () => 0,
      sleep: async () => {},
    })).rejects.toThrow('playing')
    expect(stop).toHaveBeenCalledOnce()
  })
})
