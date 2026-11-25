import type { MemberCode, MemberDetection, Slot } from '../chirp-detector'
import type { Correlation } from './detect'
import type { Observation } from './estimate'
import type { GoldProbeShape } from './probe'
import { SEARCH_S } from '../chirp-detector'
import { Correlator, decimate, findCandidates, findGlobalOffset, passesIdentity } from './detect'
import { estimateSource } from './estimate'
import { probeDuration, probeSamples } from './probe'

interface WindowPeak {
  position: number
  envelope: number
  psrDb: number
}

interface WindowResult {
  covered: boolean
  peaks: WindowPeak[]
}

// one slot's own code and every competing plan code over the same samples: another speaker's probe cannot become this
// speaker's arrival, however loud it is
function detectWindow(pcm: Float32Array, rate: number, correlators: readonly Correlator[], own: number, centre: number, reach: number, shape: Readonly<GoldProbeShape>): WindowResult {
  const first = Math.floor(centre - reach) - 20
  const last = Math.ceil(centre + reach) + 20 + correlators[own]!.refSize
  if (first < 0 || last > pcm.length || 2 * reach < 3)
    return { covered: false, peaks: [] }
  const segment = pcm.subarray(first, last)
  const correlations = Array.from({ length: correlators.length }).fill(undefined) as (Correlation | undefined)[]
  correlations[own] = correlators[own]!.correlate(segment)
  const candidates = findCandidates(correlations[own]!, centre - reach - first, centre + reach - first)
  const peaks: WindowPeak[] = []
  for (const candidate of candidates) {
    for (let other = 0; other < correlators.length; other++) {
      if (other !== own && !correlations[other])
        correlations[other] = correlators[other]!.correlate(segment)
    }
    if (!passesIdentity(correlations, own, candidate, 0.001 * rate, 3, probeDuration(shape) * rate))
      continue
    peaks.push({ position: first + candidate.lag, envelope: candidate.envelope, psrDb: candidate.psrDb })
  }
  return { covered: true, peaks }
}

function none(deviceId: string, code: MemberCode): MemberDetection {
  return {
    deviceId,
    code,
    arrivalUs: 0,
    spreadUs: 0,
    rounds: 0,
    confidence: 0,
  }
}

// the phone's coded detector: one all-slots coarse offset, narrow identity-checked measurements, then the c++ robust
// per-source consensus. no recording leaves this call (rule 8)
export function detectCodedMembers(recording: Float32Array, inputRate: number, firstExpected: number, slots: readonly Slot[], shape: Readonly<GoldProbeShape>): MemberDetection[] {
  if (slots.length === 0)
    return []
  const decimated = decimate(recording, inputRate)
  const rate = decimated.rate
  const members = [
    ...new Set(slots.map(s => s.deviceId)),
  ]
  const correlators = members.map((deviceId) => {
    const slot = slots.find(s => s.deviceId === deviceId)!
    return new Correlator(probeSamples(shape, slot.code ?? members.indexOf(deviceId), rate))
  })
  const offsets = [
    ...new Set(slots.map(s => s.offsetMs)),
  ].sort((a, b) => a - b)
  const gaps = offsets.slice(1).map((offset, i) => offset - offsets[i]!).filter(gap => gap > 0)
  const minSpacing = gaps.length > 0
    ? Math.min(...gaps) / 1000 * rate
    : Number.POSITIVE_INFINITY
  const halfWindow = Math.min(SEARCH_S * rate, Math.max(3, minSpacing / 2 - 1))
  const reach = SEARCH_S * rate
  const broad = slots.map((slot) => {
    const expected = firstExpected / decimated.stride + slot.offsetMs / 1000 * rate
    const own = members.indexOf(slot.deviceId)
    const found = detectWindow(decimated.pcm, rate, correlators, own, expected, reach + halfWindow, shape)
    return {
      expected,
      own,
      slot,
      anchor: {
        halfWindow,
        candidates: found.peaks.map(peak => ({ lag: peak.position - expected, envelope: peak.envelope, psrDb: peak.psrDb })),
      },
    }
  })
  const globalOffset = findGlobalOffset(broad.map(b => b.anchor), reach)
  const rounds = Math.max(1, ...slots.map(s => s.round + 1))
  const byMember = members.map((): Observation[] => [])
  const covered = members.map(() => false)
  for (const item of broad) {
    const expected = item.expected + globalOffset
    const found = detectWindow(decimated.pcm, rate, correlators, item.own, expected, halfWindow, shape)
    covered[item.own] ||= found.covered
    for (const peak of found.peaks) {
      byMember[item.own]!.push({
        xNs: (peak.position - expected) / rate * 1e9,
        envelope: peak.envelope,
        psrDb: peak.psrDb,
        round: item.slot.round,
        channel: 0,
      })
    }
  }
  const estimates = members.map((_, i) => estimateSource(rounds, byMember[i]!, covered[i]))
  // the wire is leader-referenced, but this reference is applied only after every slot was measured independently
  const leader = estimates[members.indexOf(slots[0]!.deviceId)]!
  const referenceNs = leader.rounds > 0
    ? leader.offsetNs
    : 0
  return members.map((deviceId, i) => {
    const own = slots.filter(s => s.deviceId === deviceId)
    const zero = own.find(s => s.round === 0)
    const estimate = estimates[i]!
    if (!zero)
      return none(deviceId, 'missed')
    if (estimate.rounds === 0)
      return none(deviceId, estimate.code)
    return {
      deviceId,
      code: estimate.code,
      arrivalUs: Math.round((estimate.offsetNs - referenceNs) / 1000 + zero.offsetMs * 1000),
      spreadUs: Math.round(estimate.spreadNs / 1000),
      rounds: estimate.rounds,
      confidence: estimate.confidence,
    }
  })
}
