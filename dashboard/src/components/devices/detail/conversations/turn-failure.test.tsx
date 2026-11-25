// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { TurnFailure } from './turn-failure'
import { failureCodeLine, failureReason } from './turn-failure-text'

afterEach(cleanup)

describe('a stored turn failure', () => {
  it('words a provider rate limit without repeating an upstream body', () => {
    const failure = {
      code: 'rate_limited',
      stage: 'llm',
      step: 1,
      provider: 'gateway-main',
      httpStatus: 429,
    } as const
    render(<TurnFailure failure={failure} />)
    expect(screen.getByText('Rate limited by provider gateway-main (HTTP 429)')).toBeTruthy()
    expect(screen.getByText('llm · step 1 · rate_limited · HTTP 429')).toBeTruthy()
  })

  it('words a provider HTTP error and its stage', () => {
    expect(failureReason({
      code: 'provider_error',
      stage: 'llm',
      provider: 'gateway-main',
      httpStatus: 502,
    })).toBe('The provider gateway-main answered HTTP 502 during the llm call')
  })

  it('names a terminal tool failure and its wait against the tool limit', () => {
    const failure = {
      code: 'timeout',
      stage: 'tool',
      step: 2,
      tool: 'weather',
      waitedMs: 10000,
      limitMs: 10000,
    } as const
    expect(failureReason(failure)).toBe('Tool call did not answer within 10 s')
    expect(failureCodeLine(failure)).toBe('tool · step 2 · timeout · waited 10.0 s of 10.0 s')
    render(<TurnFailure failure={failure} />)
    expect(screen.getByText('weather')).toBeTruthy()
  })
})
