import type { CodedPlan } from './estimate'
import type { GoldProbeShape } from './probe'
import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import reflection from '../../../../media/tests/fixtures/calibration/scene-reflection.json'
import stereo from '../../../../media/tests/fixtures/calibration/scene-stereo.json'
import { analyseCoded, fitSampleClock } from './estimate'

function pcmLe16(text: string): Int16Array {
  const bytes = Buffer.from(text, 'base64')
  return new Int16Array(bytes.buffer, bytes.byteOffset, bytes.byteLength / 2)
}

function planOf(scene: typeof stereo): CodedPlan {
  const probe: GoldProbeShape = {
    kind: 'gold',
    bandLoHz: scene.plan.probe.bandLoHz,
    bandHiHz: scene.plan.probe.bandHiHz,
    fadeUs: scene.plan.probe.fadeS * 1e6,
    amplitude: scene.plan.probe.amplitude,
  }
  return {
    members: [...scene.plan.members],
    rounds: scene.plan.rounds,
    slotNs: [...scene.plan.slotNs],
    probeCodes: [...scene.plan.probeCodes],
    priorNs: [...scene.plan.priorNs],
    priorSpanNs: [...scene.plan.priorSpanNs],
    probe,
  }
}

describe('coded scene fixtures', () => {
  for (const scene of [stereo, reflection]) {
    it(`matches ${scene.expected.map(e => e.source).join(', ')} arrivals`, () => {
      const plan = planOf(scene)
      const fitted = fitSampleClock(scene.stamps, scene.rate)
      expect(Math.abs(fitted.originNs - scene.clock.originNs)).toBeLessThanOrEqual(1)
      expect(Math.abs(fitted.nsPerFrame - scene.clock.nsPerFrame)).toBeLessThanOrEqual(1e-6)
      const heard = analyseCoded(plan, pcmLe16(scene.pcmLe16), scene.channels, scene.rate, scene.clock)
      expect(heard.map(h => h.code)).toEqual(scene.expected.map(() => 'ok'))
      for (let i = 0; i < scene.expected.length; i++) {
        const arrival = heard[i]!.tNs - plan.slotNs[i]!
        expect(Math.abs(arrival - scene.expected[i]!.arrivalNs), scene.expected[i]!.source)
          .toBeLessThanOrEqual(scene.expected[i]!.toleranceNs)
        expect(heard[i]!.ci95Ns).toBeLessThanOrEqual(2_000_000)
      }
    })
  }
})
