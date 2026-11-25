import type { TurnFailure } from '@/api/gen'

const STAGES: Readonly<Record<string, string>> = {
  stt: 'Speech recognition',
  llm: 'LLM call',
  tool: 'Tool call',
  tts: 'Speech synthesis',
  setup: 'Turn setup',
  internal: 'Turn',
}

export function failureStage(failure: TurnFailure): string {
  if (Object.hasOwn(STAGES, failure.stage))
    return STAGES[failure.stage]!
  return failure.stage
}

function seconds(ms: number): string {
  const value = ms / 1000
  if (Number.isInteger(value))
    return `${value} s`
  return `${value.toFixed(1)} s`
}

function secondsTenths(ms: number): string {
  return `${(ms / 1000).toFixed(1)} s`
}

function httpSuffix(failure: TurnFailure): string {
  if (failure.httpStatus)
    return ` (HTTP ${failure.httpStatus})`
  return ''
}

// a stored failure in words, derived from codes and numbers alone; no provider body is available here
export function failureReason(failure: TurnFailure): string {
  const stage = failureStage(failure)
  let provider = 'the provider'
  if (failure.provider)
    provider = `provider ${failure.provider}`
  switch (failure.code) {
    case 'timeout': {
      const limit = failure.limitMs ?? failure.waitedMs
      let subject = stage
      if (failure.stage === 'llm')
        subject = 'LLM gateway'
      if (limit)
        return `${subject} did not answer within ${seconds(limit)}`
      return `${subject} did not answer in time`
    }
    case 'rate_limited':
      return `Rate limited by ${provider}${httpSuffix(failure)}`
    case 'auth':
      return `Authentication refused by ${provider}${httpSuffix(failure)}`
    case 'provider_error':
      if (failure.httpStatus)
        return `The ${provider} answered HTTP ${failure.httpStatus} during the ${stage.toLowerCase()}`
      return `The ${provider} failed during the ${stage.toLowerCase()}`
    case 'budget':
      return 'The agent exhausted the turn’s step or time budget'
    case 'canceled':
    case 'cancelled':
      return `The turn was cancelled during the ${stage.toLowerCase()}`
    case 'not_configured':
      return `The provider for ${stage.toLowerCase()} is not configured`
    case 'stt_failed':
    case 'stt_unavailable':
      return 'Speech recognition failed before the agent could answer'
    case 'tts_failed':
      return 'The reply was written, but speech synthesis failed'
    default: {
      let tool = ''
      if (failure.tool)
        tool = ` in ${failure.tool}`
      return `${stage} failed${tool}${httpSuffix(failure)}`
    }
  }
}

// one stable code line for support: stage, step, code, upstream status, and the wait against its limit
export function failureCodeLine(failure: TurnFailure): string {
  const parts: string[] = [failure.stage]
  if (failure.step) {
    parts.push(`step ${failure.step}`)
  }
  parts.push(failure.code)
  if (failure.httpStatus) {
    parts.push(`HTTP ${failure.httpStatus}`)
  }
  if (failure.waitedMs) {
    parts.push(failure.limitMs
      ? `waited ${secondsTenths(failure.waitedMs)} of ${secondsTenths(failure.limitMs)}`
      : `waited ${secondsTenths(failure.waitedMs)}`)
  }
  else if (failure.limitMs) {
    parts.push(`limit ${secondsTenths(failure.limitMs)}`)
  }
  return parts.join(' · ')
}
