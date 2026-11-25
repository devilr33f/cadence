// @vitest-environment jsdom
import { cleanup, render } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { useNow } from './use-now'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

function Clock({ seen }: { seen: (number | null)[] }) {
  seen.push(useNow())
  return null
}

it('reads the time as it is from the first render, also after the clock stood still', () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-12T15:00:00Z'))
  const first: (number | null)[] = []
  const { unmount } = render(<Clock seen={first} />)
  expect(first[0]).toBe(Date.parse('2026-11-12T15:00:00Z'))
  unmount()

  vi.setSystemTime(new Date('2026-11-12T16:00:00Z'))
  const second: (number | null)[] = []
  render(<Clock seen={second} />)
  expect(second[0]).toBe(Date.parse('2026-11-12T16:00:00Z'))
})
