import type { Fields, Level } from './log.ts'
import { expect, it } from 'vitest'
import { quietConsole } from './console.ts'

it('turns every console call into a line of its level and an error\'s class, never what it was given', () => {
  const lines: Array<Fields & { level: Level, msg: string }> = []
  const target = { ...console }
  quietConsole((level, msg, fields = {}) => {
    lines.push({ level, msg, ...fields })
  }, target)

  target.error('Failed to render %s', '/devices?token=cdn_s3cret', new TypeError('the owner\'s transcript'))
  target.warn('hydration', { password: 'hunter2-hunter2' })
  target.log('ok')
  target.error(Object.assign(new Error('x'), { name: 'Bad name with spaces' }))

  expect(lines).toEqual([
    {
      level: 'ERROR',
      msg: 'console',
      method: 'error',
      error: 'TypeError',
    },
    { level: 'WARN', msg: 'console', method: 'warn' },
    { level: 'INFO', msg: 'console', method: 'log' },
    {
      level: 'ERROR',
      msg: 'console',
      method: 'error',
      error: 'Error',
    },
  ])
  const text = JSON.stringify(lines)
  for (const secret of [
    'cdn_s3cret',
    'transcript',
    'hunter2',
    'Failed',
    'devices',
  ])
    expect(text).not.toContain(secret)
})
