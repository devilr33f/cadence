// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ApplyBadge } from './apply-badge'
import { applyState } from './apply-state'
import { DeviceStatus } from './device-status'
import { SeverityLabel } from './severity-label'
import { StatusBadge } from './status-badge'

afterEach(cleanup)

function badge(name: RegExp | string) {
  return screen.getByText(name).closest('[data-tone]') as HTMLElement
}

describe('status badge', () => {
  it.each([
    ['success', 'Online'],
    ['warning', 'Degraded'],
    ['danger', 'Failed'],
    ['info', 'Updating'],
    ['neutral', 'Offline'],
  ] as const)('a %s badge reads %s in text, the colour is never the only signal', (tone, label) => {
    render(<StatusBadge tone={tone} label={label} />)
    const element = badge(label)
    expect(element.dataset.tone).toBe(tone)
    expect(element.querySelector('svg[aria-hidden="true"]')).toBeTruthy()
  })
})

describe('device status', () => {
  it.each([
    ['online', 'Online', 'success'],
    ['offline', 'Offline', 'neutral'],
    ['updating', 'Updating', 'info'],
    ['enrolling', 'Enrolling', 'neutral'],
  ] as const)('%s reads %s', (status, label, tone) => {
    render(<DeviceStatus status={status} />)
    expect(badge(label).dataset.tone).toBe(tone)
  })
})

describe('desired vs applied', () => {
  it.each([
    [
      { desiredGen: 14, appliedGen: 14, appliedError: '' },
      'applied',
    ],
    [
      { desiredGen: 15, appliedGen: 14, appliedError: '' },
      'pending',
    ],
    [
      { desiredGen: 15, appliedGen: 15, appliedError: 'invalid_config' },
      'failed',
    ],
    [
      { desiredGen: 16, appliedGen: 15, appliedError: 'invalid_config' },
      'pending',
    ],
  ])('%o is %s', (generations, state) => {
    expect(applyState(generations)).toBe(state)
  })

  it('names an applied generation', () => {
    render(<ApplyBadge desiredGen={14} appliedGen={14} appliedError="" />)
    expect(badge('Applied').dataset.tone).toBe('success')
    expect(screen.getByText('gen 14')).toBeTruthy()
  })

  it('shows a pending change travelling from the applied generation to the desired one', () => {
    render(<ApplyBadge desiredGen={15} appliedGen={14} appliedError="" />)
    expect(badge('Pending').dataset.tone).toBe('info')
    expect(screen.getByText('gen 14 → 15')).toBeTruthy()
  })

  it('keeps a failed generation visible with the device code', () => {
    render(<ApplyBadge desiredGen={15} appliedGen={15} appliedError="invalid_config" />)
    expect(badge('Failed').dataset.tone).toBe('danger')
    expect(screen.getByText('invalid_config')).toBeTruthy()
  })
})

describe('log severity', () => {
  it.each([
    ['debug', 'DEBUG', 'neutral'],
    ['info', 'INFO', 'info'],
    ['warn', 'WARN', 'warning'],
    ['error', 'ERROR', 'danger'],
  ] as const)('%s reads %s', (severity, label, tone) => {
    render(<SeverityLabel severity={severity} />)
    expect(badge(label).dataset.tone).toBe(tone)
  })
})
