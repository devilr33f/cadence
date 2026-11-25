import type { CalibrateBoostRequest, CalibrationMemberView, CalibrationView } from '@/api/gen'
import type { ListenerPhase } from '@/lib/listener-calibration'
import { describeError } from '@/api/describe-error'

// a boost's step and ceiling, dB (pkg/capschema GroupBoostMaxDb)
export const BOOST_STEP_DB = 6
export const BOOST_MAX_DB = 14

// the codes a louder chirp may help: heard faintly, not at all, or not the same each round
const FAINT = new Set([
  'weak',
  'missed',
  'unstable',
  'no_self',
  'isolated',
])

// a member a retry should play louder: one of FAINT, or measured with a low confidence
export function needsBoost(m: CalibrationMemberView): boolean {
  return FAINT.has(m.code) || (m.code === 'ok' && m.confidence < 0.5)
}

// the boosts of a retry: each member that needs one 6 dB more than it had (at most 14), the others as they were; none
// when every member that needs one is at the ceiling already
export function nextBoosts(calibration: CalibrationView, boosts: Readonly<Record<string, number>>): Record<string, number> | undefined {
  const faint = calibration.members.filter(needsBoost)
  if (faint.length === 0 || faint.every(m => (boosts[m.deviceId] ?? 0) >= BOOST_MAX_DB))
    return undefined
  const out = { ...boosts }
  for (const m of faint)
    out[m.deviceId] = Math.min(BOOST_MAX_DB, (boosts[m.deviceId] ?? 0) + BOOST_STEP_DB)
  return out
}

export function boostList(boosts: Readonly<Record<string, number>>): CalibrateBoostRequest[] {
  return Object.entries(boosts)
    .filter(([, gainDb]) => gainDb > 0)
    .map(([deviceId, gainDb]) => ({ deviceId, gainDb }))
}

export const PHASES: Readonly<Record<ListenerPhase, string>> = {
  starting: 'Starting the microphone…',
  listening: 'Listening: keep still and quiet…',
  measuring: 'Measuring…',
}

// what a microphone that would not start says, in words: its error's name alone, never its message
export function failureText(failure: unknown): string {
  if (failure instanceof DOMException) {
    if (failure.name === 'NotAllowedError' || failure.name === 'SecurityError')
      return 'The microphone was not allowed. Allow it for this site and try again.'
    if (failure.name === 'NotFoundError')
      return 'No microphone was found.'
    return `The microphone could not start (${failure.name}).`
  }
  return describeError(failure)
}
