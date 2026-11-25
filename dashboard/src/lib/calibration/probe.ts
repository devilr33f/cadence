// gold-coded calibration probes, sample-for-sample with media/lib/src/sync/calibration/probe.cpp. pure: no audio or dom

export const GOLD_CHIPS = 1023
export const GOLD_PULSE_CHIPS = 8
export const GOLD_ROLL_OFF = 0.25

export interface GoldProbeShape {
  kind: 'gold'
  bandLoHz: number
  bandHiHz: number
  fadeUs: number
  // a fraction of full scale on the music input, never a device volume (rule 7)
  amplitude?: number
}

export const DEFAULT_GOLD_PROBE: Readonly<GoldProbeShape> = {
  kind: 'gold',
  bandLoHz: 1000,
  bandHiHz: 6000,
  fadeUs: 10_000,
  amplitude: 0.1,
}

function mSequence(second: boolean): Int8Array {
  const out = new Int8Array(GOLD_CHIPS)
  const state = new Int8Array(10)
  state[0] = 1
  for (let n = 0; n < out.length; n++) {
    out[n] = state[0]!
    const next = second
      ? state[8]! ^ state[3]! ^ state[2]! ^ state[0]!
      : state[3]! ^ state[0]!
    for (let k = 0; k + 1 < state.length; k++)
      state[k] = state[k + 1]!
    state[state.length - 1] = next
  }
  return out
}

const FIRST = mSequence(false)
const SECOND = mSequence(true)

// bpsk +1/-1; code k is m1 xor (m2 rotated by k)
export function goldCode(code: number): Int8Array {
  const shift = ((code % GOLD_CHIPS) + GOLD_CHIPS) % GOLD_CHIPS
  const out = new Int8Array(GOLD_CHIPS)
  for (let n = 0; n < out.length; n++)
    out[n] = 1 - 2 * (FIRST[n]! ^ SECOND[(n + shift) % GOLD_CHIPS]!)
  return out
}

function sinc(x: number): number {
  return Math.abs(x) < 1e-12
    ? 1
    : Math.sin(Math.PI * x) / (Math.PI * x)
}

function raisedCosine(chips: number): number {
  if (Math.abs(chips) > GOLD_PULSE_CHIPS)
    return 0
  // the denominator's |x|=2 singularity is removable
  if (Math.abs(Math.abs(chips) - 2) < 1e-10)
    return Math.PI / 4 * sinc(chips)
  return sinc(chips) * Math.cos(Math.PI * GOLD_ROLL_OFF * chips)
    / (1 - (2 * GOLD_ROLL_OFF * chips) ** 2)
}

export function probeDuration(shape: Readonly<GoldProbeShape>): number {
  const chipRate = shape.bandHiHz - shape.bandLoHz
  return chipRate > 0
    ? (GOLD_CHIPS + 2 * GOLD_PULSE_CHIPS) / chipRate
    : 0
}

// sample t seconds after the probe starts, independently of every other sample
export function probeAt(shape: Readonly<GoldProbeShape>, code: number, tS: number): number {
  const duration = probeDuration(shape)
  const chipRate = shape.bandHiHz - shape.bandLoHz
  if (tS < 0 || tS >= duration || chipRate <= 0)
    return 0
  const chips = goldCode(code)
  const at = tS * chipRate - GOLD_PULSE_CHIPS
  const first = Math.max(0, Math.floor(at) - GOLD_PULSE_CHIPS)
  const last = Math.min(GOLD_CHIPS - 1, Math.ceil(at) + GOLD_PULSE_CHIPS)
  let baseband = 0
  for (let k = first; k <= last; k++)
    baseband += chips[k]! * raisedCosine(at - k)
  const fadeS = shape.fadeUs / 1e6
  let fade = 1
  if (fadeS > 0) {
    const edge = Math.min(tS, duration - tS)
    if (edge < fadeS)
      fade = 0.5 - 0.5 * Math.cos(Math.PI * edge / fadeS)
  }
  // the carrier is global, not restarted per chip: correlation sees the same passband waveform on both sides
  return (shape.amplitude ?? 0.1) * fade * baseband
    * Math.cos(2 * Math.PI * (shape.bandLoHz + shape.bandHiHz) / 2 * tS)
}

export function probeSamples(shape: Readonly<GoldProbeShape>, code: number, rate: number): Float32Array {
  const out = new Float32Array(Math.ceil(probeDuration(shape) * rate))
  for (let n = 0; n < out.length; n++)
    out[n] = probeAt(shape, code, n / rate)
  return out
}
