import { findCandidates, findGlobalOffset } from './calibration/detect'

// the listener calibration's detector: each member's chirps found in the browser's recording by a matched filter. the
// chirp and the detector follow media/lib/src/sync/calibration.cpp (chirpAt, correlate, interpolate, findArrival) so
// the browser hears what the speakers play. pure: no clock, no audio kept past a call (rule 8)

// the sweep every member plays (pkg/capschema GroupChirp*, calibration.h ChirpShape): exponential from f0 to f1,
// raised-cosine fades, at -20 dBFS (a boost plays it louder; the shape is the same)
export interface ChirpShape {
  f0Hz: number
  f1Hz: number
  durationUs: number
  fadeUs: number
}

// what the schedule names now
export const CHIRP: Readonly<ChirpShape> = {
  f0Hz: 800,
  f1Hz: 7000,
  durationUs: 250_000,
  fadeUs: 10_000,
}

// what a leader from before rounds plays: its schedule names no chirp
export const LEGACY_CHIRP: Readonly<ChirpShape> = {
  f0Hz: 1000,
  f1Hz: 6000,
  durationUs: 100_000,
  fadeUs: 5_000,
}

export const CHIRP_AMPLITUDE = 0.1

// the search either side of where a chirp is expected (the slots are 1.2 s apart)
export const SEARCH_S = 0.6
// the second pass: each round looked for again this close to the member's median, and kept this close to the new one
const REFINE_S = 0.002
const KEEP_S = 0.001
// a member whose kept rounds spread past this is unstable
const MAX_SPREAD_S = 0.0005

// calibration.h DetectorConfig: ok over minPsrDb; the direct path is the earliest peak this close to the highest
const MIN_PSR_DB = 15
const EARLIEST_FRACTION = 0.5
const ZERO_CONFIDENCE_DB = 10
const FULL_CONFIDENCE_DB = 40
const MAX_PSR_DB = 200
// a rayleigh envelope's median is this many sigmas of either component
const RAYLEIGH_MEDIAN = 1.1774100225154747
// calibration.cpp: the windowed sinc's half width, in lags, and the fine search's steps per lag
const SINC_HALF = 16
const FINE_STEPS = 32

export function chirpAt(tS: number, shape: Readonly<ChirpShape> = CHIRP): number {
  const durationS = shape.durationUs / 1e6
  const fadeS = shape.fadeUs / 1e6
  if (tS < 0 || tS >= durationS)
    return 0
  const ln = Math.log(shape.f1Hz / shape.f0Hz)
  const phase = 2 * Math.PI * shape.f0Hz * durationS / ln * (Math.exp(tS / durationS * ln) - 1)
  let gain = 1
  const edge = Math.min(tS, durationS - tS)
  if (edge < fadeS)
    gain = 0.5 - 0.5 * Math.cos(Math.PI * edge / fadeS)
  return CHIRP_AMPLITUDE * gain * Math.sin(phase)
}

// the sweep at rate: sample n is chirpAt(n / rate)
export function chirpSamples(rate: number, shape: Readonly<ChirpShape> = CHIRP): Float32Array {
  const n = Math.ceil(shape.durationUs / 1e6 * rate)
  const out = new Float32Array(n)
  for (let i = 0; i < n; i++)
    out[i] = chirpAt(i / rate, shape)
  return out
}

function nextPow2(n: number): number {
  let p = 1
  while (p < n)
    p *= 2
  return p
}

// in-place radix-2 fft over split real and imaginary arrays; the inverse is unnormalized
export function fft(re: Float64Array, im: Float64Array, inverse: boolean) {
  const n = re.length
  for (let i = 1, j = 0; i < n; i++) {
    let bit = n >> 1
    for (; (j & bit) !== 0; bit >>= 1)
      j ^= bit
    j ^= bit
    if (i < j) {
      [re[i], re[j]] = [re[j]!, re[i]!]
      ;[im[i], im[j]] = [im[j]!, im[i]!]
    }
  }
  for (let len = 2; len <= n; len *= 2) {
    const angle = (inverse
      ? 2
      : -2) * Math.PI / len
    const half = len / 2
    // each twiddle computed once per stage, never by recurrence: no rounding grows over a long transform
    for (let k = 0; k < half; k++) {
      const wr = Math.cos(angle * k)
      const wi = Math.sin(angle * k)
      for (let i = 0; i < n; i += len) {
        const a = i + k
        const b = a + half
        const vr = re[b]! * wr - im[b]! * wi
        const vi = re[b]! * wi + im[b]! * wr
        re[b] = re[a]! - vr
        im[b] = im[a]! - vi
        re[a] = re[a]! + vr
        im[a] = im[a]! + vi
      }
    }
  }
}

// the analytic cross-correlation of signal with ref for lags [0, signal.length - ref.length] (calibration.cpp
// correlate): its envelope's peak is where ref starts in signal whatever the path did to its phase. spectra caches
// ref's transform by size, so a run's many windows transform it once
export interface Correlation {
  re: Float64Array
  im: Float64Array
}

export function correlate(signal: Float32Array, ref: Float32Array, spectra = new Map<number, Correlation>()): Correlation {
  if (ref.length === 0 || ref.length > signal.length) {
    return {
      re: new Float64Array(0),
      im: new Float64Array(0),
    }
  }
  const n = nextPow2(signal.length + ref.length)
  let r = spectra.get(n)
  if (!r) {
    r = {
      re: new Float64Array(n),
      im: new Float64Array(n),
    }
    r.re.set(ref)
    fft(r.re, r.im, false)
    spectra.set(n, r)
  }
  const sr = new Float64Array(n)
  const si = new Float64Array(n)
  sr.set(signal)
  fft(sr, si, false)
  // S * conj(R), its negative frequencies zeroed and the positive doubled: the analytic correlation
  for (let k = 0; k < n; k++) {
    const xr = sr[k]! * r.re[k]! + si[k]! * r.im[k]!
    const xi = si[k]! * r.re[k]! - sr[k]! * r.im[k]!
    const scale = k === 0 || k === n / 2
      ? 1
      : k < n / 2
        ? 2
        : 0
    sr[k] = xr * scale
    si[k] = xi * scale
  }
  fft(sr, si, true)
  const lags = signal.length - ref.length + 1
  const re = new Float64Array(lags)
  const im = new Float64Array(lags)
  for (let k = 0; k < lags; k++) {
    re[k] = sr[k]! / n
    im[k] = si[k]! / n
  }
  return { re, im }
}

// |c[k]| of correlate
export function correlationEnvelope(signal: Float32Array, ref: Float32Array): Float64Array {
  const c = correlate(signal, ref)
  const out = new Float64Array(c.re.length)
  for (let k = 0; k < out.length; k++)
    out[k] = Math.hypot(c.re[k]!, c.im[k]!)
  return out
}

// the correlation's envelope at a fractional lag, by a blackman-windowed sinc over its neighbours (calibration.cpp
// interpolate)
export function envelopeAt(c: Correlation, lag: number): number {
  const base = Math.floor(lag)
  let sr = 0
  let si = 0
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
    const w = sinc * (0.42 + 0.5 * Math.cos(Math.PI * u) + 0.08 * Math.cos(2 * Math.PI * u))
    sr += c.re[k]! * w
    si += c.im[k]! * w
  }
  return Math.hypot(sr, si)
}

// the envelope's peak near lag to a fraction of a lag: a fine grid over its neighbours, then a parabola through the
// grid's best and its two neighbours (calibration.cpp findArrival)
function refine(c: Correlation, lag: number): number {
  const step = 1 / FINE_STEPS
  const lo = Math.max(lag - 1, 0)
  const hi = Math.min(lag + 1, c.re.length - 1)
  let bestLag = lag
  let best = -1
  for (let x = lo; x <= hi + 1e-9; x += step) {
    const e = envelopeAt(c, x)
    if (e > best) {
      best = e
      bestLag = x
    }
  }
  const left = envelopeAt(c, bestLag - step)
  const right = envelopeAt(c, bestLag + step)
  const denominator = left - 2 * best + right
  if (denominator < 0)
    bestLag += step * 0.5 * (left - right) / denominator
  return bestLag
}

export type ChirpCode = 'ok' | 'weak' | 'missed'

export interface Detection {
  code: ChirpCode
  // the recording's fractional sample where the chirp starts (ok and weak)
  position: number
  psrDb: number
  confidence: number
}

const MISSED: Detection = {
  code: 'missed',
  position: 0,
  psrDb: 0,
  confidence: 0,
}

function floorMedian(values: Float64Array): number {
  const sorted = Float64Array.from(values).sort()
  return sorted[Math.floor(sorted.length / 2)]!
}

// one search window of the recording: its correlation, envelope and noise floor
interface Window {
  from: number
  c: Correlation
  envelope: Float64Array
  highest: number
  sigma: number
}

function searchWindow(recording: Float32Array, rate: number, expected: number, ref: Float32Array, spectra?: Map<number, Correlation>, reach = Math.round(SEARCH_S * rate)): Window | undefined {
  const from = Math.max(0, Math.round(expected) - reach)
  const to = Math.min(recording.length, Math.round(expected) + reach + ref.length)
  if (to - from < ref.length + 3)
    return undefined
  const c = correlate(recording.subarray(from, to), ref, spectra)
  const envelope = new Float64Array(c.re.length)
  let highest = 0
  for (let k = 0; k < envelope.length; k++) {
    envelope[k] = Math.hypot(c.re[k]!, c.im[k]!)
    highest = Math.max(highest, envelope[k]!)
  }
  return {
    from,
    c,
    envelope,
    highest,
    sigma: floorMedian(envelope) / RAYLEIGH_MEDIAN,
  }
}

// the chirp in the window's lags [lo, hi]: the earliest envelope peak at least half their highest (the direct path,
// not a reflection), ok above MIN_PSR_DB over the window's floor (refined to a fraction of a sample), weak above
// ZERO_CONFIDENCE_DB, else missed. a narrowed range's peak must also stand at least half the window's highest: a
// sidelobe of a chirp outside the range is no chirp
function pick(w: Window, lo: number, hi: number): Detection {
  const { envelope } = w
  const whole = lo <= 0 && hi >= envelope.length - 1
  lo = Math.max(0, Math.ceil(lo))
  hi = Math.min(envelope.length - 1, Math.floor(hi))
  if (hi < lo)
    return MISSED
  let highest = 0
  for (let i = lo; i <= hi; i++)
    highest = Math.max(highest, envelope[i]!)
  if (highest <= 0 || (!whole && highest < EARLIEST_FRACTION * w.highest))
    return MISSED
  // a range that only climbs toward a peak outside it holds no chirp of its own
  let at = -1
  for (let i = lo; i <= hi; i++) {
    const e = envelope[i]!
    const peak = (i === 0 || e >= envelope[i - 1]!) && (i + 1 === envelope.length || e >= envelope[i + 1]!)
    if (peak && e >= EARLIEST_FRACTION * highest) {
      at = i
      break
    }
  }
  if (at < 0)
    return MISSED
  const psrDb = w.sigma <= 0
    ? MAX_PSR_DB
    : Math.min(MAX_PSR_DB, 20 * Math.log10(envelope[at]! / w.sigma))
  const confidence = Math.min(1, Math.max(0, (psrDb - ZERO_CONFIDENCE_DB) / (FULL_CONFIDENCE_DB - ZERO_CONFIDENCE_DB)))
  const code: ChirpCode = psrDb >= MIN_PSR_DB
    ? 'ok'
    : psrDb >= ZERO_CONFIDENCE_DB
      ? 'weak'
      : 'missed'
  return {
    code,
    position: w.from + (code === 'ok'
      ? refine(w.c, at)
      : at),
    psrDb,
    confidence,
  }
}

// one chirp near `expected` (a sample of the recording), within +-SEARCH_S; missed when the window is not in the
// recording
export function detectChirp(recording: Float32Array, rate: number, expected: number, ref = chirpSamples(rate)): Detection {
  const w = searchWindow(recording, rate, expected, ref)
  return w
    ? pick(w, 0, w.envelope.length - 1)
    : MISSED
}

// one slot of a listener calibration's schedule (CalibrationSlotView)
export interface Slot {
  deviceId: string
  offsetMs: number
  round: number
  // the coded plan's gold code; legacy identical-chirp schedules leave it absent
  code?: number
}

export type MemberCode = ChirpCode | 'unstable'

export interface MemberDetection {
  deviceId: string
  code: MemberCode
  // the median of its kept rounds' arrivals less their slots, plus its round-0 slot, against the leader's round-0
  // chirp, us: what apply-group-calibration takes (the leader's own is about 0); 0 unless ok or unstable
  arrivalUs: number
  // the kept rounds' standard deviation, us
  spreadUs: number
  // the rounds kept
  rounds: number
  confidence: number
}

function median(values: readonly number[]): number {
  const s = [...values].sort((a, b) => a - b)
  const mid = Math.floor(s.length / 2)
  return s.length % 2 === 1
    ? s[mid]!
    : (s[mid - 1]! + s[mid]!) / 2
}

function sampleStd(values: readonly number[]): number {
  if (values.length < 2)
    return 0
  const mean = values.reduce((a, b) => a + b, 0) / values.length
  return Math.sqrt(values.reduce((a, b) => a + (b - mean) ** 2, 0) / (values.length - 1))
}

// every member's chirps: one coarse global offset scored over all slots, then each slot measured inside less than half
// the slot spacing minus the chirp's length, so a silent member cannot borrow its neighbour's identical chirp. per
// member, pass 1 takes the median of the ok rounds; pass 2 narrows around it and keeps the rounds that agree
export function detectMembers(recording: Float32Array, rate: number, firstExpected: number, slots: readonly Slot[], shape: Readonly<ChirpShape> = CHIRP): MemberDetection[] {
  if (slots.length === 0)
    return []
  const ref = chirpSamples(rate, shape)
  const spectra = new Map<number, Correlation>()
  const offsets = [
    ...new Set(slots.map(s => s.offsetMs)),
  ].sort((a, b) => a - b)
  const gaps = offsets.slice(1).map((offset, i) => offset - offsets[i]!).filter(gap => gap > 0)
  const minSpacing = gaps.length > 0
    ? Math.min(...gaps) / 1000 * rate
    : Number.POSITIVE_INFINITY
  const halfWindow = Math.max(3, Math.floor(minSpacing / 2 - ref.length - 1))
  const reach = Math.round(SEARCH_S * rate)
  const broad = slots.map((slot) => {
    const expected = firstExpected + slot.offsetMs / 1000 * rate
    const w = searchWindow(recording, rate, expected, ref, spectra, Math.round(reach + halfWindow))
    const candidates = w
      ? findCandidates(w.c, 0, w.c.re.length, w.sigma).map(candidate => ({
          lag: w.from + candidate.lag - expected,
          envelope: candidate.envelope,
          psrDb: candidate.psrDb,
        }))
      : []
    return {
      slot,
      expected,
      anchor: { candidates, halfWindow },
    }
  })
  const globalOffset = findGlobalOffset(broad.map(b => b.anchor), reach)
  const byMember = new Map<string, Slot[]>()
  for (const s of slots) {
    byMember.set(s.deviceId, [
      ...(byMember.get(s.deviceId) ?? []),
      s,
    ])
  }
  const rounds = Math.max(1, ...slots.map(s => s.round + 1))
  const minRounds = Math.max(1, rounds - 1)
  const refine = Math.round(REFINE_S * rate)
  const keep = KEEP_S * rate
  const found = [...byMember].map(([deviceId, own]): MemberDetection => {
    const none = (code: MemberCode, confidence: number): MemberDetection => ({
      deviceId,
      code,
      arrivalUs: 0,
      spreadUs: 0,
      rounds: 0,
      confidence,
    })
    const zero = own.find(s => s.round === 0)
    if (!zero)
      return none('missed', 0)
    const tries = own.map((s) => {
      const expected = firstExpected + s.offsetMs / 1000 * rate + globalOffset
      const w = searchWindow(recording, rate, expected, ref, spectra, halfWindow)
      const detected = w
        ? pick(w, 0, w.envelope.length - 1)
        : MISSED
      return { expected, w, found: detected }
    })
    const heard = tries.filter(t => t.found.code === 'ok')
    if (heard.length === 0) {
      const best = Math.max(0, ...tries.map(t => t.found.confidence))
      return none(tries.some(t => t.found.code === 'weak')
        ? 'weak'
        : 'missed', best)
    }
    const m = median(heard.map(t => t.found.position - t.expected))
    const second = tries.flatMap((t) => {
      if (!t.w)
        return []
      // lag k is the recording's sample w.from + k: x = w.from + k - expected
      const centre = t.expected + m - t.w.from
      const detected = pick(t.w, centre - refine, centre + refine)
      return detected.code === 'ok'
        ? [
            { x: detected.position - t.expected, confidence: detected.confidence },
          ]
        : []
    })
    const m2 = second.length > 0
      ? median(second.map(s => s.x))
      : m
    const kept = second.filter(s => Math.abs(s.x - m2) <= keep)
    const spread = sampleStd(kept.map(s => s.x))
    const stable = kept.length >= minRounds && spread <= MAX_SPREAD_S * rate
    const xs = kept.length > 0
      ? kept.map(s => s.x)
      : heard.map(t => t.found.position - t.expected)
    return {
      deviceId,
      code: stable
        ? 'ok'
        : 'unstable',
      arrivalUs: Math.round((median(xs) / rate + zero.offsetMs / 1000) * 1e6),
      spreadUs: Math.round(spread / rate * 1e6),
      rounds: kept.length,
      confidence: kept.length > 0
        ? median(kept.map(s => s.confidence))
        : median(heard.map(t => t.found.confidence)),
    }
  })
  // arrivals stay leader-referenced on the wire, but the leader is only a reporting reference after the all-slots search
  const leader = found.find(m => m.deviceId === slots[0]!.deviceId && m.rounds > 0)
  if (leader) {
    const zero = slots.find(s => s.deviceId === leader.deviceId && s.round === 0)!
    const reference = leader.arrivalUs - zero.offsetMs * 1000
    for (const member of found) {
      if (member.rounds > 0)
        member.arrivalUs = Math.round(member.arrivalUs - reference)
    }
  }
  return found
}
