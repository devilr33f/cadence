import type { GoldProbeShape } from './calibration/probe'
import type { ChirpShape } from './chirp-detector'
import type { Recorder } from './listener-recorder'
import type { CalibrationProbeView, CalibrationSlotView, CalibrationView, HeardMemberRequest } from '@/api/gen'
import { detectCodedMembers } from './calibration/coded'
import { probeDuration } from './calibration/probe'
import { CHIRP, detectMembers, LEGACY_CHIRP } from './chirp-detector'

export type ListenerProbe = CalibrationProbeView
export type ListenerSlot = CalibrationSlotView
export type ListenerCalibrationView = CalibrationView

// how long the recording runs past the last chirp's expected end
const TAIL_MS = 1000

export interface ListenerDeps {
  // starts the microphone; called first, synchronously, inside the owner's press
  record: () => Promise<Recorder>
  // calibrate-group in listener mode (with the owner's boosts, if any)
  calibrate: () => Promise<ListenerCalibrationView>
  // apply-group-calibration
  apply: (calibrationId: string, members: HeardMemberRequest[]) => Promise<CalibrationView>
  // performance.now
  now: () => number
  sleep: (ms: number) => Promise<void>
}

export type ListenerPhase = 'starting' | 'listening' | 'measuring'

// the sweep a schedule names: its own, else (a leader from before rounds) the legacy one
export function scheduleChirp(scheduled: ListenerCalibrationView): ChirpShape {
  const c = scheduled.chirp
  if (!c || c.f0Hz <= 0 || c.f1Hz <= c.f0Hz || c.durationUs <= 0) {
    return scheduled.rounds
      ? { ...CHIRP }
      : { ...LEGACY_CHIRP }
  }
  return { ...c }
}

// a listener calibration, start to end, in the browser: the microphone records before the chirps are asked for; the
// answer's arrival less half the request's round trip, plus startsInMs and each slot's offset, says where each chirp
// should be; the recording runs a second past the last slot's chirp, then every track stops. the detector's arrivals
// (numbers only) are posted, and the recording is dropped with this call (rule 8)
export async function runListenerCalibration(deps: ListenerDeps, onPhase?: (phase: ListenerPhase) => void): Promise<CalibrationView> {
  onPhase?.('starting')
  const recorder = await deps.record()
  try {
    onPhase?.('listening')
    const sent = deps.now()
    const scheduled = await deps.calibrate()
    const arrived = deps.now()
    const frameAtAnswer = recorder.frameNow()
    if (scheduled.code !== 'scheduled' || !scheduled.calibrationId || !scheduled.slots?.length)
      return scheduled
    const shape = scheduleChirp(scheduled)
    const gold: GoldProbeShape | undefined = scheduled.probe?.kind === 'gold'
      ? {
          kind: 'gold',
          bandLoHz: scheduled.probe.bandLoHz,
          bandHiHz: scheduled.probe.bandHiHz,
          fadeUs: scheduled.probe.fadeUs,
          amplitude: 0.1,
        }
      : undefined
    const slots = scheduled.slots.map(s => ({
      deviceId: s.deviceId,
      offsetMs: s.offsetMs,
      round: s.round ?? 0,
      code: s.code,
    }))
    const oneWayMs = (arrived - sent) / 2
    const firstInMs = (scheduled.startsInMs ?? 0) - oneWayMs
    const probeMs = gold
      ? probeDuration(gold) * 1000
      : shape.durationUs / 1000
    const lastMs = firstInMs + Math.max(...slots.map(s => s.offsetMs)) + probeMs + TAIL_MS
    await deps.sleep(Math.max(0, lastMs - (deps.now() - arrived)))
    onPhase?.('measuring')
    const members = (() => {
      const recording = recorder.stop()
      const found = gold
        ? detectCodedMembers(recording, recorder.rate, frameAtAnswer + firstInMs / 1000 * recorder.rate, slots, gold)
        : detectMembers(recording, recorder.rate, frameAtAnswer + firstInMs / 1000 * recorder.rate, slots, shape)
      return found.map((m): HeardMemberRequest => ({
        deviceId: m.deviceId,
        code: m.code,
        arrivalUs: m.arrivalUs,
        confidence: Math.round(m.confidence * 1000) / 1000,
        spreadUs: m.spreadUs,
        rounds: m.rounds,
      }))
    })()
    return await deps.apply(scheduled.calibrationId, members)
  }
  finally {
    recorder.stop()
  }
}
