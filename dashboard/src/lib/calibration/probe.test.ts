import type { GoldProbeShape } from './probe'
import { describe, expect, it } from 'vitest'
import fixture from '../../../../media/tests/fixtures/calibration/probes.json'
import { goldCode, probeAt, probeDuration, probeSamples } from './probe'

const shape: GoldProbeShape = {
  kind: 'gold',
  bandLoHz: fixture.shape.bandLoHz,
  bandHiHz: fixture.shape.bandHiHz,
  fadeUs: fixture.shape.fadeS * 1e6,
  amplitude: fixture.shape.amplitude,
}

describe('gold probe fixtures', () => {
  it('matches every fixture sample, count, sum and energy', () => {
    for (const expected of fixture.probes) {
      const pcm = probeSamples(shape, expected.code, expected.rate)
      expect(pcm.length).toBe(expected.count)
      expect(pcm.length).toBe(Math.ceil(probeDuration(shape) * expected.rate))
      for (let k = 0; k < expected.first64.length; k++)
        expect(Math.abs(pcm[k]! - expected.first64[k]!)).toBeLessThanOrEqual(1e-6)
      let sum = 0
      let energy = 0
      for (const sample of pcm) {
        sum += sample
        energy += sample * sample
      }
      expect(Math.abs(sum - expected.sum)).toBeLessThanOrEqual(1e-6)
      expect(Math.abs(energy - expected.energy)).toBeLessThanOrEqual(1e-6)
    }
  })

  it('uses the documented preferred pair and closes the probe outside its duration', () => {
    const sequence = (second: boolean) => {
      const state = [1, 0, 0, 0, 0, 0, 0, 0, 0, 0]
      return Array.from({ length: 1023 }, () => {
        const out = state[0]!
        const next = second
          ? state[8]! ^ state[3]! ^ state[2]! ^ state[0]!
          : state[3]! ^ state[0]!
        for (let k = 0; k + 1 < state.length; k++)
          state[k] = state[k + 1]!
        state[state.length - 1] = next
        return out
      })
    }
    const first = sequence(false)
    const second = sequence(true)
    const code = goldCode(7)
    expect(code.length).toBe(1023)
    for (let n = 0; n < code.length; n++)
      expect(code[n]).toBe(1 - 2 * (first[n]! ^ second[(n + 7) % 1023]!))
    expect(probeAt(shape, 0, -0.001)).toBe(0)
    expect(probeAt(shape, 0, probeDuration(shape))).toBe(0)
  })
})
