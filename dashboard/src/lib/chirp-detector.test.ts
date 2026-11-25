import type { ChirpShape, Slot } from './chirp-detector'
import { describe, expect, it } from 'vitest'
import { CHIRP, chirpAt, chirpSamples, detectChirp, detectMembers, LEGACY_CHIRP } from './chirp-detector'

const RATE = 48000

// a seeded gaussian noise, so every run hears the same room
function noise(n: number, sigma: number, seed = 7): Float32Array {
  let s = seed
  const uniform = () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return (s + 1) / 4294967297
  }
  const out = new Float32Array(n)
  for (let i = 0; i < n; i++)
    out[i] = sigma * Math.sqrt(-2 * Math.log(uniform())) * Math.cos(2 * Math.PI * uniform())
  return out
}

// adds a chirp starting at a fractional sample, scaled
function addChirp(into: Float32Array, at: number, gain: number, shape: ChirpShape = CHIRP) {
  const length = Math.ceil(shape.durationUs / 1e6 * RATE) + 2
  for (let i = Math.max(0, Math.floor(at)); i < Math.min(into.length, Math.floor(at) + length); i++)
    into[i]! += gain * chirpAt((i - at) / RATE, shape)
}

const seconds = (s: number) => s * RATE

// rounds x members slots, round-major, 1.2 s apart, the leader first
function schedule(members: readonly string[], rounds: number): Slot[] {
  const out: Slot[] = []
  for (let r = 0; r < rounds; r++) {
    members.forEach((deviceId, k) => out.push({ deviceId, round: r, offsetMs: (r * members.length + k) * 1200 }))
  }
  return out
}

describe('chirp', () => {
  it('is the device\'s sweep: 250 ms, faded in and out, at -20 dBFS', () => {
    const c = chirpSamples(RATE)
    expect(c.length).toBe(12000)
    expect(c[0]).toBe(0)
    let peak = 0
    for (const v of c)
      peak = Math.max(peak, Math.abs(v))
    expect(peak).toBeGreaterThan(0.099)
    expect(peak).toBeLessThanOrEqual(Math.fround(0.1))
    expect(chirpAt(0.25)).toBe(0)
    expect(chirpAt(-0.001)).toBe(0)
  })

  it('keeps the legacy sweep for a leader before rounds', () => {
    expect(chirpSamples(RATE, LEGACY_CHIRP).length).toBe(4800)
    expect(chirpAt(0.1, LEGACY_CHIRP)).toBe(0)
  })
})

describe('detectChirp', () => {
  it('finds a chirp to a fraction of a sample, offset from where it was expected', () => {
    for (const shiftMs of [3, -42, 250, -480]) {
      const rec = noise(seconds(3), 0.005)
      const at = seconds(1.5) + shiftMs / 1000 * RATE + 0.37
      addChirp(rec, at, 0.4)
      const found = detectChirp(rec, RATE, seconds(1.5))
      expect(found.code).toBe('ok')
      expect(Math.abs(found.position - at) / RATE * 1e6).toBeLessThan(5)
      expect(found.confidence).toBeGreaterThan(0.5)
    }
  })

  it('takes the direct path over a louder reflection that comes after it', () => {
    const rec = noise(seconds(3), 0.003)
    const at = seconds(1.2) + 0.25
    addChirp(rec, at, 0.3)
    addChirp(rec, at + seconds(0.012), 0.45)
    const found = detectChirp(rec, RATE, seconds(1.25))
    expect(found.code).toBe('ok')
    expect(Math.abs(found.position - at)).toBeLessThan(1)
  })

  it('misses a chirp that never sounded, or one outside the recording', () => {
    const rec = noise(seconds(3), 0.01)
    expect(detectChirp(rec, RATE, seconds(1.5)).code).not.toBe('ok')
    expect(detectChirp(rec, RATE, seconds(10)).code).toBe('missed')
    expect(detectChirp(new Float32Array(seconds(3)), RATE, seconds(1.5)).code).toBe('missed')
  })
})

describe('detectMembers', () => {
  it('takes each member\'s median over its rounds against its round-0 slot, dropping an outlier round', () => {
    const members = ['lead', 'b', 'c']
    const slots = schedule(members, 4)
    const rec = noise(seconds(16), 0.004)
    const first = seconds(1.0) + 0.6
    // each member's lateness per round, ms: b is 37.5 ms late, its third round a reflection 5 ms later still; c is
    // 120 ms early, and silent in its last round
    const late: Record<string, number[]> = {
      lead: [0, 0.02, -0.03, 0.01],
      b: [37.5, 37.52, 42.5, 37.47],
      c: [-120, -120.03, -119.98, Number.NaN],
    }
    for (const s of slots) {
      const ms = late[s.deviceId]![s.round]!
      if (!Number.isNaN(ms)) {
        addChirp(rec, first + seconds((s.offsetMs + ms) / 1000), s.deviceId === 'b'
          ? 0.2
          : 0.3)
      }
    }
    // the browser's estimate of the first is 200 ms off
    const got = detectMembers(rec, RATE, first + seconds(0.2), slots)
    expect(got.map(m => m.deviceId)).toEqual(members)
    expect(got.map(m => m.code)).toEqual(['ok', 'ok', 'ok'])
    expect(Math.abs(got[0]!.arrivalUs)).toBeLessThan(30)
    expect(got[0]!.rounds).toBe(4)
    expect(Math.abs(got[1]!.arrivalUs - (1_200_000 + 37_500))).toBeLessThan(30)
    expect(got[1]!.rounds).toBe(3)
    expect(got[1]!.spreadUs).toBeLessThan(60)
    expect(Math.abs(got[2]!.arrivalUs - (2_400_000 - 120_000))).toBeLessThan(30)
    expect(got[2]!.rounds).toBe(3)
    expect(got[2]!.confidence).toBeGreaterThan(0.5)
  })

  it('calls a member whose rounds disagree unstable, and one never heard missed', () => {
    const slots = schedule(['lead', 'b', 'c'], 4)
    const rec = noise(seconds(16), 0.004)
    const first = seconds(1.0)
    const drift = [0, 3, 6, 9]
    for (const s of slots) {
      if (s.deviceId === 'lead')
        addChirp(rec, first + seconds(s.offsetMs / 1000), 0.3)
      if (s.deviceId === 'b')
        addChirp(rec, first + seconds((s.offsetMs + drift[s.round]!) / 1000), 0.3)
    }
    const got = detectMembers(rec, RATE, first, slots)
    expect(got.map(m => m.code)).toEqual([
      'ok',
      'unstable',
      expect.stringMatching(/^(weak|missed)$/),
    ])
    expect(got[2]!.arrivalUs).toBe(0)
    expect(got[2]!.rounds).toBe(0)
  })

  it('measures a leader before rounds as before: one round of the legacy sweep', () => {
    const slots = schedule(['lead', 'b'], 1).map(s => ({ ...s, offsetMs: s.offsetMs / 1200 * 1500 }))
    const rec = noise(seconds(5), 0.004)
    const first = seconds(1.0) + 0.3
    addChirp(rec, first, 0.3, LEGACY_CHIRP)
    addChirp(rec, first + seconds(1.5 + 0.021), 0.3, LEGACY_CHIRP)
    const got = detectMembers(rec, RATE, first + seconds(0.1), slots, LEGACY_CHIRP)
    expect(got.map(m => [m.code, m.rounds, m.spreadUs])).toEqual([
      ['ok', 1, 0],
      ['ok', 1, 0],
    ])
    expect(got[0]!.arrivalUs).toBe(0)
    expect(Math.abs(got[1]!.arrivalUs - 1_521_000)).toBeLessThan(15)
  })

  it('never gives a silent member a neighbour\'s chirp or moves the other arrivals', () => {
    const members = ['lead', 'b', 'c']
    const slots = schedule(members, 2)
    const first = seconds(1.0) + 0.6
    const late: Record<string, number> = { lead: 0, b: 37.5, c: -120 }
    const make = (silent?: string) => {
      const rec = noise(seconds(9), 0.004)
      for (const s of slots) {
        if (s.deviceId !== silent) {
          addChirp(rec, first + seconds((s.offsetMs + late[s.deviceId]!) / 1000), s.deviceId === 'lead'
            ? 0.8
            : 0.3)
        }
      }
      return rec
    }
    const before = detectMembers(make(), RATE, first + seconds(0.2), slots)
    expect(before.map(m => m.code)).toEqual(['ok', 'ok', 'ok'])
    const after = detectMembers(make('b'), RATE, first + seconds(0.2), slots)
    expect(after.map(m => m.code)).toEqual(['ok', 'missed', 'ok'])
    expect(after[1]!.arrivalUs).toBe(0)
    expect(Math.abs(after[0]!.arrivalUs - before[0]!.arrivalUs)).toBeLessThanOrEqual(30)
    expect(Math.abs(after[2]!.arrivalUs - before[2]!.arrivalUs)).toBeLessThanOrEqual(30)
  })

  it('finds a 300 ms late member without anchoring on its silent leader', () => {
    const slots = schedule(['lead', 'b'], 2)
    const first = seconds(1.0)
    const rec = noise(seconds(7), 0.004)
    for (const s of slots) {
      if (s.deviceId === 'b')
        addChirp(rec, first + seconds((s.offsetMs + 300) / 1000), 0.3)
    }
    const got = detectMembers(rec, RATE, first, slots)
    expect(got.map(m => m.code)).toEqual(['missed', 'ok'])
    expect(Math.abs(got[1]!.arrivalUs - 1_500_000)).toBeLessThanOrEqual(50)
  })
})
