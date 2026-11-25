import type { DetectorConfig } from './detect'
import type { GoldProbeShape } from './probe'
import { Correlator, decimate, findCandidates, passesIdentity } from './detect'
import { probeDuration, probeSamples } from './probe'

export type ArrivalCode = 'ok' | 'weak' | 'missed' | 'unstable'

export interface CodedPlan {
  members: readonly string[]
  rounds: number
  // round-major, on the leader's timeline
  slotNs: readonly number[]
  probeCodes: readonly number[]
  priorNs: readonly number[]
  priorSpanNs: readonly number[]
  probe: GoldProbeShape
}

export interface ReadStamp {
  endFrame: number
  monoNs: number
}

export interface SampleClock {
  originNs: number
  nsPerFrame: number
}

export interface AnalysisConfig {
  detector: DetectorConfig
  toleranceNs: number
  clusterNs: number
  minEnvelopeFraction: number
  maxMicChannels: number
  identityMarginDb: number
}

export const ANALYSIS_CONFIG: Readonly<AnalysisConfig> = {
  detector: {
    minPsrDb: 15,
    earliestFraction: 0.5,
    zeroConfidenceDb: 10,
    fullConfidenceDb: 40,
  },
  toleranceNs: 2_000_000,
  clusterNs: 250_000,
  minEnvelopeFraction: 0.25,
  maxMicChannels: 3,
  identityMarginDb: 3,
}

export interface Observation {
  xNs: number
  envelope: number
  psrDb: number
  round: number
  channel: number
}

export interface Heard {
  code: ArrivalCode
  // the source's arrival on the leader timeline; the browser uses offsetNs instead
  tNs: number
  offsetNs: number
  confidence: number
  spreadNs: number
  sigmaNs: number
  ci95Ns: number
  rounds: number
}

export function median(values: readonly number[]): number {
  if (values.length === 0)
    return 0
  const sorted = [...values].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length % 2 === 1
    ? sorted[mid]!
    : (sorted[mid - 1]! + sorted[mid]!) / 2
}

function madSigma(values: readonly number[], centre: number): number {
  return 1.4826 * median(values.map(value => Math.abs(value - centre)))
}

function confidence(psr: number, config: Readonly<DetectorConfig>): number {
  return Math.min(1, Math.max(0, (psr - config.zeroConfidenceDb) / (config.fullConfidenceDb - config.zeroConfidenceDb)))
}

interface Cluster {
  observations: number[]
  meanNs: number
  meanEnvelope: number
  rounds: number
}

function clusters(observations: Observation[], radiusNs: number, rounds: number): Cluster[] {
  observations.sort((a, b) => a.xNs - b.xNs)
  const out: Cluster[] = []
  for (let k = 0; k < observations.length; k++) {
    const observation = observations[k]!
    if (out.length === 0 || Math.abs(observation.xNs - out[out.length - 1]!.meanNs) > radiusNs) {
      out.push({
        observations: [],
        meanNs: observation.xNs,
        meanEnvelope: observation.envelope,
        rounds: 0,
      })
    }
    else {
      const cluster = out[out.length - 1]!
      const count = cluster.observations.length
      cluster.meanNs = (cluster.meanNs * count + observation.xNs) / (count + 1)
      cluster.meanEnvelope = (cluster.meanEnvelope * count + observation.envelope) / (count + 1)
    }
    out[out.length - 1]!.observations.push(k)
  }
  for (const cluster of out) {
    const seen = new Set<number>()
    for (const index of cluster.observations) {
      const round = observations[index]!.round
      if (round >= 0 && round < rounds)
        seen.add(round)
    }
    cluster.rounds = seen.size
  }
  return out
}

const EMPTY = {
  tNs: 0,
  offsetNs: 0,
  confidence: 0,
  spreadNs: 0,
  sigmaNs: 0,
  ci95Ns: 0,
  rounds: 0,
}

// the c++ direct-path consensus: earliest sufficiently supported cluster, then median/MAD and a robust 95% interval
export function estimateSource(rounds: number, candidates: readonly Observation[], covered = true, config: Readonly<AnalysisConfig> = ANALYSIS_CONFIG): Heard {
  const minDirect = Math.max(Math.ceil(rounds / 2), 1)
  const minKept = Math.max(minDirect, 2)
  if (candidates.length === 0) {
    return {
      ...EMPTY,
      code: covered ? 'missed' : 'missed',
    }
  }
  const sorted = [...candidates]
  const groups = clusters(sorted, config.clusterNs, rounds)
  const strongest = Math.max(...groups.map(group => group.meanEnvelope))
  const direct = groups.find(group => group.rounds >= minDirect && group.meanEnvelope >= config.minEnvelopeFraction * strongest)
  if (!direct)
    return { ...EMPTY, code: 'unstable' }

  const roundNs = new Float64Array(rounds)
  const present = Array.from({ length: rounds }).fill(false) as boolean[]
  const confidences = new Float64Array(rounds)
  for (let r = 0; r < rounds; r++) {
    const byChannel = new Map<number, Observation>()
    for (const index of direct.observations) {
      const observation = sorted[index]!
      if (observation.round !== r)
        continue
      const old = byChannel.get(observation.channel)
      // one voice per microphone: a near-in sidelobe must not outweigh another microphone's main peak
      if (!old || observation.envelope > old.envelope)
        byChannel.set(observation.channel, observation)
    }
    const own = [
      ...byChannel.values(),
    ]
    if (own.length > 0) {
      present[r] = true
      roundNs[r] = median(own.map(o => o.xNs))
      confidences[r] = confidence(median(own.map(o => o.psrDb)), config.detector)
    }
  }
  const all = [
    ...present.flatMap((yes, r) => yes
      ? [roundNs[r]!]
      : []),
  ]
  const centre = median(all)
  const gate = Math.max(3 * madSigma(all, centre), 300_000)
  const kept: number[] = []
  const keptConfidence: number[] = []
  for (let r = 0; r < rounds; r++) {
    if (present[r] && Math.abs(roundNs[r]! - centre) <= gate) {
      kept.push(roundNs[r]!)
      keptConfidence.push(confidences[r]!)
    }
  }
  const absolute = median(kept)
  const sigma = madSigma(kept, absolute)
  const ci95Ns = kept.length === 0
    ? 0
    : 1.96 * Math.max(sigma, 10_000) / Math.sqrt(kept.length)
  return {
    code: kept.length >= minKept && ci95Ns <= config.toleranceNs
      ? 'ok'
      : 'unstable',
    tNs: 0,
    offsetNs: absolute,
    confidence: median(keptConfidence),
    spreadNs: sigma,
    sigmaNs: sigma,
    ci95Ns,
    rounds: kept.length,
  }
}

function slot(plan: CodedPlan, round: number, member: number): number {
  return plan.slotNs[round * plan.members.length + member]!
}

function sourceWindowFromNs(plan: CodedPlan, round: number, member: number): number {
  return slot(plan, round, member) + (plan.priorNs[member] ?? 0) - (plan.priorSpanNs[member] ?? 150_000_000) - 20_000_000
}

function sourceWindowToNs(plan: CodedPlan, round: number, member: number): number {
  return slot(plan, round, member) + (plan.priorNs[member] ?? 0) + (plan.priorSpanNs[member] ?? 150_000_000) + 20_000_000
}

// a read returns late, never early: fit the lower convex hull, then refit its nearest tenth by least squares
export function fitSampleClock(stamps: readonly ReadStamp[], nominalRate: number, maxPpm = 1000): SampleClock {
  const nominal = 1e9 / nominalRate
  if (stamps.length === 0)
    return { originNs: 0, nsPerFrame: nominal }
  const x0 = stamps[0]!.endFrame
  const y0 = stamps[0]!.monoNs
  const points = stamps.map(s => ({ x: s.endFrame - x0, y: s.monoNs - y0 }))
    .sort((a, b) => a.x - b.x || a.y - b.y)
  const hull: typeof points = []
  for (const p of points) {
    while (hull.length >= 2) {
      const a = hull[hull.length - 2]!
      const b = hull[hull.length - 1]!
      const cross = (b.x - a.x) * (p.y - a.y) - (b.y - a.y) * (p.x - a.x)
      if (cross > 0)
        break
      hull.pop()
    }
    if (hull.length > 0 && hull[hull.length - 1]!.x === p.x) {
      if (p.y < hull[hull.length - 1]!.y)
        hull[hull.length - 1] = p
      continue
    }
    hull.push(p)
  }
  let slope = nominal
  if (hull.length >= 2) {
    const mean = points.reduce((sum, p) => sum + p.x, 0) / points.length
    let edge = 0
    while (edge + 2 < hull.length && hull[edge + 1]!.x < mean)
      edge++
    const a = hull[edge]!
    const b = hull[edge + 1]!
    let candidate = (b.y - a.y) / (b.x - a.x)
    const base = a.y - candidate * a.x
    const gaps = points.map(p => ({ gap: p.y - (base + candidate * p.x), x: p.x }))
      .sort((a, b) => a.gap - b.gap || a.x - b.x)
    const keep = Math.max(Math.floor(points.length / 10), Math.min(points.length, 3))
    const nearest = gaps.slice(0, keep)
    const mx = nearest.reduce((sum, p) => sum + p.x, 0) / keep
    const my = nearest.reduce((sum, p) => sum + p.gap, 0) / keep
    let sxx = 0
    let sxy = 0
    for (const p of nearest) {
      sxx += (p.x - mx) ** 2
      sxy += (p.x - mx) * (p.gap - my)
    }
    if (sxx > 0)
      candidate += sxy / sxx
    if (Math.abs(candidate / nominal - 1) <= maxPpm * 1e-6)
      slope = candidate
  }
  let intercept = points[0]!.y - slope * points[0]!.x
  for (const p of points)
    intercept = Math.min(intercept, p.y - slope * p.x)
  return { originNs: y0 + intercept - slope * x0, nsPerFrame: slope }
}

// exact coded-scene analysis used by the shared c++/typescript fixtures. the phone has no self-arrival, so this never
// computes the c++ relative-to-self values
export function analyseCoded(plan: CodedPlan, interleaved: Int16Array, channels: number, rate: number, clock: Readonly<SampleClock>, toLeader: (localMonoNs: number) => number = value => value, config: Readonly<AnalysisConfig> = ANALYSIS_CONFIG): Heard[] {
  const sources = plan.members.length
  if (sources === 0 || channels === 0 || plan.slotNs.length < plan.rounds * sources)
    return Array.from({ length: sources }, () => ({ ...EMPTY, code: 'weak' as const }))
  const toLocal = (leaderNs: number) => {
    let local = leaderNs
    for (let k = 0; k < 4; k++)
      local += leaderNs - toLeader(local)
    return local
  }
  const monoAt = (frame: number) => clock.originNs + frame * clock.nsPerFrame
  const frameAt = (monoNs: number) => (monoNs - clock.originNs) / clock.nsPerFrame
  // these fixtures are 16 khz or decimated to it, matching the c++ analyser's fixed reference rate
  const correlators = plan.members.map((_, i) => new Correlator(probeSamples(plan.probe, plan.probeCodes[i] ?? i, 16000)))
  const observations = Array.from({ length: channels }, () => Array.from({ length: sources }, (): Observation[] => []))
  const covered = Array.from({ length: sources }, () => Array.from({ length: plan.rounds }).fill(false) as boolean[])
  const quality = new Float64Array(channels)
  const frames = Math.floor(interleaved.length / channels)
  for (let ch = 0; ch < channels; ch++) {
    const input = new Float32Array(frames)
    for (let n = 0; n < frames; n++)
      input[n] = interleaved[n * channels + ch]! / 32768
    const decimated = decimate(input, rate)
    for (let i = 0; i < sources; i++) {
      for (let r = 0; r < plan.rounds; r++) {
        const from = frameAt(toLocal(sourceWindowFromNs(plan, r, i))) / decimated.stride
        const to = frameAt(toLocal(sourceWindowToNs(plan, r, i))) / decimated.stride
        const first = Math.floor(from) - 20
        const last = Math.ceil(to) + 20 + correlators[i]!.refSize
        if (first < 0 || last > decimated.pcm.length || to - from < 3)
          continue
        covered[i]![r] = true
        const segment = decimated.pcm.subarray(first, last)
        const correlations = Array.from({ length: sources }).fill(undefined) as (ReturnType<Correlator['correlate']> | undefined)[]
        correlations[i] = correlators[i]!.correlate(segment)
        const candidates = findCandidates(correlations[i]!, from - first, to - first, 0, config.detector)
        for (const candidate of candidates) {
          if (plan.probe.kind === 'gold') {
            for (let other = 0; other < sources; other++) {
              if (other !== i && !correlations[other])
                correlations[other] = correlators[other]!.correlate(segment)
            }
            if (!passesIdentity(correlations, i, candidate, 0.001 * decimated.rate, config.identityMarginDb, probeDuration(plan.probe) * decimated.rate))
              continue
          }
          const frame = (first + candidate.lag) * decimated.stride
          const xNs = toLeader(monoAt(frame)) - slot(plan, r, i)
          observations[ch]![i]!.push({
            xNs,
            envelope: candidate.envelope,
            psrDb: candidate.psrDb,
            round: r,
            channel: ch,
          })
          quality[ch] = Math.max(quality[ch]!, candidate.psrDb)
        }
      }
    }
  }
  const selected = Array.from({ length: channels }, (_, i) => i)
    .sort((a, b) => quality[b]! - quality[a]! || a - b)
    .slice(0, Math.min(channels, Math.max(config.maxMicChannels, 1), 3))
  return plan.members.map((_, i) => {
    const candidates = selected.flatMap(ch => observations[ch]![i]!)
    const heard = estimateSource(plan.rounds, candidates, covered[i]!.some(Boolean), config)
    const tNs = slot(plan, 0, i) + heard.offsetNs
    return {
      ...heard,
      tNs: tNs < 0
        ? -Math.round(-tNs)
        : Math.round(tNs),
    }
  })
}
