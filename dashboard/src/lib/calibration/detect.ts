import type { Correlation } from './fft'
import { Correlator } from './fft'

const SINC_HALF = 16
const FINE_STEPS = 32
const RAYLEIGH_MEDIAN = 1.1774100225154747

export interface DetectorConfig {
  minPsrDb: number
  earliestFraction: number
  zeroConfidenceDb: number
  fullConfidenceDb: number
}

export const DETECTOR_CONFIG: Readonly<DetectorConfig> = {
  minPsrDb: 15,
  earliestFraction: 0.5,
  zeroConfidenceDb: 10,
  fullConfidenceDb: 40,
}

export interface Decimated {
  pcm: Float32Array
  rate: number
  stride: number
}

// 32 and 48 khz use the c++ zero-phase windowed sinc. 44.1 khz deliberately stays at 44.1 rather than paying for a
// fractional resampler; coded references are then rendered at that same rate
export function decimate(pcm: Float32Array, rate: number): Decimated {
  const integer = Math.abs(rate - 32000) < 0.01 || Math.abs(rate - 48000) < 0.01
  if (rate <= 16000 || !integer) {
    return {
      pcm: new Float32Array(pcm),
      rate,
      stride: 1,
    }
  }
  const factor = Math.round(rate / 16000)
  const half = 4 * factor
  const taps = new Float32Array(2 * half + 1)
  const cutoff = 0.45 / factor
  let sum = 0
  for (let k = -half; k <= half; k++) {
    const x = cutoff * k
    const sinc = Math.abs(x) < 1e-12
      ? 1
      : Math.sin(Math.PI * x) / (Math.PI * x)
    const window = 0.54 + 0.46 * Math.cos(Math.PI * k / half)
    const h = cutoff * sinc * window
    taps[k + half] = h
    sum += h
  }
  for (let k = 0; k < taps.length; k++)
    taps[k] = taps[k]! / sum
  const out = new Float32Array(Math.floor(pcm.length / factor))
  for (let n = 0; n < out.length; n++) {
    const centre = n * factor
    let sample = 0
    for (let k = -half; k <= half; k++) {
      const at = centre + k
      if (at >= 0 && at < pcm.length)
        sample = Math.fround(sample + Math.fround(taps[k + half]! * pcm[at]!))
    }
    out[n] = sample
  }
  return { pcm: out, rate: 16000, stride: factor }
}

export function interpolate(c: Correlation, lag: number): { re: number, im: number } {
  const base = Math.floor(lag)
  let re = 0
  let im = 0
  for (let k = base - SINC_HALF + 1; k <= base + SINC_HALF; k++) {
    if (k < 0 || k >= c.re.length)
      continue
    const x = lag - k
    const u = x / SINC_HALF
    if (Math.abs(u) >= 1)
      continue
    const sinc = Math.abs(x) < 1e-12
      ? 1
      : Math.sin(Math.PI * x) / (Math.PI * x)
    const window = 0.42 + 0.5 * Math.cos(Math.PI * u) + 0.08 * Math.cos(2 * Math.PI * u)
    const w = sinc * window
    re += c.re[k]! * w
    im += c.im[k]! * w
  }
  return { re, im }
}

export function envelopeAt(c: Correlation, lag: number): number {
  const at = interpolate(c, lag)
  return Math.hypot(at.re, at.im)
}

export interface Peak {
  lag: number
  envelope: number
  psrDb: number
}

function psrDb(peak: number, sigma: number): number {
  if (sigma <= 0) {
    return peak > 0
      ? 200
      : 0
  }
  return Math.min(200, 20 * Math.log10(peak / sigma))
}

// every local envelope peak over minPsrDb in [from,to), sinc-refined; sigma<=0 takes the window's rayleigh floor
export function findCandidates(c: Correlation, from: number, to: number, sigma = 0, config: Readonly<DetectorConfig> = DETECTOR_CONFIG): Peak[] {
  const out: Peak[] = []
  if (from < 0 || to > c.re.length || to - from < 3)
    return out
  const first = Math.ceil(from)
  const last = Math.floor(to) - 1
  const envelope = new Float64Array(last - first + 1)
  for (let k = first; k <= last; k++)
    envelope[k - first] = Math.hypot(c.re[k]!, c.im[k]!)
  if (sigma <= 0) {
    const sorted = Float64Array.from(envelope).sort()
    sigma = sorted[Math.floor(sorted.length / 2)]! / RAYLEIGH_MEDIAN
  }
  for (let i = 0; i < envelope.length; i++) {
    const e = envelope[i]!
    const peak = (i === 0 || e >= envelope[i - 1]!) && (i + 1 === envelope.length || e >= envelope[i + 1]!)
    const psr = psrDb(e, sigma)
    if (!peak || psr < config.minPsrDb)
      continue
    const centre = first + i
    const lo = Math.max(centre - 1, 0)
    const hi = Math.min(centre + 1, c.re.length - 1)
    const step = 1 / FINE_STEPS
    let bestLag = centre
    let best = -1
    for (let x = lo; x <= hi + 1e-9; x += step) {
      const refined = envelopeAt(c, x)
      if (refined > best) {
        best = refined
        bestLag = x
      }
    }
    const left = envelopeAt(c, bestLag - step)
    const right = envelopeAt(c, bestLag + step)
    const denominator = left - 2 * best + right
    if (denominator < 0)
      bestLag += step * 0.5 * (left - right) / denominator
    out.push({ lag: bestLag, envelope: best, psrDb: psr })
  }
  return out
}

// another code within 1 ms and less than 3 db below a candidate owns it; coverLags also rejects its sidelobes under a
// stronger code's whole probe. undefined correlations are codes not evaluated for this window
export function passesIdentity(correlations: readonly (Correlation | undefined)[], expected: number, candidate: Peak, radiusLags: number, marginDb = 3, coverLags = 0): boolean {
  if (expected >= correlations.length || !correlations[expected])
    return false
  const threshold = candidate.envelope * 10 ** (-marginDb / 20)
  for (let other = 0; other < correlations.length; other++) {
    if (other === expected)
      continue
    const c = correlations[other]
    if (!c)
      continue
    const rejectIn = (radius: number) => {
      const from = Math.max(0, Math.ceil(candidate.lag - radius))
      const to = Math.min(c.re.length, Math.floor(candidate.lag + radius) + 1)
      for (let lag = from; lag < to; lag++) {
        if (Math.hypot(c.re[lag]!, c.im[lag]!) > threshold)
          return true
      }
      return false
    }
    if (rejectIn(radiusLags) || (coverLags > radiusLags && rejectIn(coverLags)))
      return false
  }
  return true
}

export interface AnchorSlot {
  // candidates' lags are recording samples less this slot's network-estimated position
  candidates: readonly Peak[]
  halfWindow: number
}

// one global offset for every slot, never a leader-only anchor. the coarse score is the sum of each slot's best
// identity-checked peak; ties prefer more supported slots, then the offset closest to the network estimate
export function findGlobalOffset(slots: readonly AnchorSlot[], reach: number): number {
  if (slots.length === 0 || reach <= 0)
    return 0
  const step = Math.max(1, Math.round(reach / 1200))
  const steps = Math.ceil(2 * reach / step)
  let bestOffset = 0
  let bestScore = -1
  let bestSupport = 0
  for (let k = 0; k <= steps; k++) {
    const offset = Math.min(reach, -reach + k * step)
    let score = 0
    let support = 0
    for (const slot of slots) {
      let strongest = 0
      for (const candidate of slot.candidates) {
        if (Math.abs(candidate.lag - offset) <= slot.halfWindow)
          strongest = Math.max(strongest, candidate.envelope)
      }
      if (strongest > 0) {
        score += strongest
        support++
      }
    }
    const tied = Math.abs(score - bestScore) <= 1e-12 * Math.max(1, Math.abs(score))
    const better = score > bestScore && !tied
    const tieBetter = tied && (support > bestSupport || (support === bestSupport && Math.abs(offset) < Math.abs(bestOffset)))
    if (better || tieBetter) {
      bestOffset = offset
      bestScore = score
      bestSupport = support
    }
  }
  return bestOffset
}

export { Correlator }
export type { Correlation }
