import type { Slot } from '../chirp-detector'
import type { GoldProbeShape } from './probe'
import { describe, expect, it } from 'vitest'
import { detectCodedMembers } from './coded'
import { decimate } from './detect'
import { probeAt, probeDuration } from './probe'

const RATE = 16000
const GOLD: GoldProbeShape = {
  kind: 'gold',
  bandLoHz: 1000,
  bandHiHz: 6000,
  fadeUs: 10_000,
  amplitude: 0.1,
}

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

function addProbe(into: Float32Array, at: number, code: number, gain: number) {
  const length = Math.ceil(probeDuration(GOLD) * RATE) + 2
  for (let i = Math.max(0, Math.floor(at)); i < Math.min(into.length, Math.floor(at) + length); i++)
    into[i]! += gain * probeAt(GOLD, code, (i - at) / RATE)
}

function schedule(members: readonly string[], rounds: number): Slot[] {
  const out: Slot[] = []
  for (let r = 0; r < rounds; r++) {
    members.forEach((deviceId, k) => out.push({
      deviceId,
      round: r,
      offsetMs: (r * members.length + k) * 1200,
      code: k,
    }))
  }
  return out
}

function recording(slots: readonly Slot[], silent?: string) {
  const rec = noise(RATE * 9, 0.002)
  const first = RATE + 0.6
  const late: Record<string, number> = { lead: 0, b: 37.5, c: -120 }
  for (const s of slots) {
    if (s.deviceId === silent)
      continue
    addProbe(rec, first + (s.offsetMs + late[s.deviceId]!) / 1000 * RATE, s.code ?? 0, s.deviceId === 'lead'
      ? 0.8
      : 0.3)
  }
  return { rec, first }
}

describe('decimation', () => {
  it('keeps 44.1 khz at its own rate, as the c++ detector does', () => {
    const pcm = new Float32Array([0, 0.25, -0.5, 1])
    const out = decimate(pcm, 44100)
    expect(out.rate).toBe(44100)
    expect(out.stride).toBe(1)
    expect([...out.pcm]).toEqual([...pcm])
  })
})

describe('coded listener attribution', () => {
  it('leaves a silent member missed and does not move any other member', () => {
    const slots = schedule(['lead', 'b', 'c'], 2)
    const baseline = recording(slots)
    const before = detectCodedMembers(baseline.rec, RATE, baseline.first + RATE * 0.2, slots, GOLD)
    expect(before.map(m => m.code)).toEqual(['ok', 'ok', 'ok'])
    const silent = recording(slots, 'b')
    const after = detectCodedMembers(silent.rec, RATE, silent.first + RATE * 0.2, slots, GOLD)
    expect(after.map(m => m.code)).toEqual(['ok', 'missed', 'ok'])
    expect(after[1]!.arrivalUs).toBe(0)
    expect(after[1]!.rounds).toBe(0)
    expect(Math.abs(after[0]!.arrivalUs - before[0]!.arrivalUs)).toBeLessThanOrEqual(30)
    expect(Math.abs(after[2]!.arrivalUs - before[2]!.arrivalUs)).toBeLessThanOrEqual(30)
  })

  it('finds the other codes when the leader is silent', () => {
    const slots = schedule(['lead', 'b', 'c'], 2)
    const silent = recording(slots, 'lead')
    const got = detectCodedMembers(silent.rec, RATE, silent.first, slots, GOLD)
    expect(got.map(m => m.code)).toEqual(['missed', 'ok', 'ok'])
    expect(Math.abs(got[1]!.arrivalUs - 1_237_500)).toBeLessThanOrEqual(50)
    expect(Math.abs(got[2]!.arrivalUs - 2_280_000)).toBeLessThanOrEqual(50)
  })

  it('finds a 300 ms late speaker in its own slot', () => {
    const slots = schedule(['lead', 'b'], 2)
    const rec = noise(RATE * 6, 0.002)
    const first = RATE + 0.6
    for (const s of slots) {
      addProbe(rec, first + (s.offsetMs + (s.deviceId === 'b'
        ? 300
        : 0)) / 1000 * RATE, s.code ?? 0, 0.3)
    }
    const got = detectCodedMembers(rec, RATE, first + RATE * 0.1, slots, GOLD)
    expect(got.map(m => m.code)).toEqual(['ok', 'ok'])
    expect(Math.abs(got[1]!.arrivalUs - 1_500_000)).toBeLessThanOrEqual(50)
  })
})
