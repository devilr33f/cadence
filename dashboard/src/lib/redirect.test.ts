import { expect, it } from 'vitest'
import { safeRedirect } from './redirect'

it.each([
  ['/devices', '/devices'],
  ['/devices/d1?tab=log#timeline', '/devices/d1?tab=log#timeline'],
  ['/', '/'],
])('returns to the same-origin path %s', (value, expected) => {
  expect(safeRedirect(value)).toBe(expected)
})

it.each([
  [undefined],
  [''],
  ['devices'],
  ['https://evil.example/'],
  ['//evil.example/'],
  ['/\\evil.example/'],
  ['\\\\evil.example'],
  ['/\t/evil.example'],
  ['javascript:alert(1)'],
  ['/login?redirect=/devices'],
  ['/.//evil.example'],
  ['/..//evil.example'],
  ['/%2e//evil.example'],
  ['/a/..//evil.example'],
  ['/%2E%2E//evil.example/x?y#z'],
])('refuses %j and goes home', (value) => {
  expect(safeRedirect(value)).toBe('/')
})
