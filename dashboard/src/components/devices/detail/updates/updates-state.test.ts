import { describe, expect, it } from 'vitest'
import { kindNeedsVersion, kindOfBoard, rollbackRun, updateAvailable } from './updates-state'

describe('kindOfBoard', () => {
  it('maps each board to its artifact kind', () => {
    expect(kindOfBoard('yandex-midi')).toBe('tar')
    expect(kindOfBoard('yandex-micro')).toBe('tar')
    expect(kindOfBoard('yandex-pickle')).toBe('tar')
    expect(kindOfBoard('android-tv')).toBe('apk')
    expect(kindOfBoard('rpi')).toBe('deb')
  })
  it('asks a version only where the file cannot carry it', () => {
    expect(kindNeedsVersion(kindOfBoard('rpi'))).toBe(true)
    expect(kindNeedsVersion(kindOfBoard('android-tv'))).toBe(true)
    expect(kindNeedsVersion(kindOfBoard('yandex-midi'))).toBe(false)
  })
})

describe('rollbackRun', () => {
  const run = (status: string, at: string, id = 'a') => ({ status, startedAt: at, artifactId: id })
  it('picks the newest succeeded run with an artifact', () => {
    expect(rollbackRun([
      run('failed', '3'),
      run('succeeded', '2'),
      run('succeeded', '1', 'b'),
    ]))
      .toMatchObject({ startedAt: '2' })
  })
  it('answers null without one', () => {
    expect(rollbackRun([
      run('running', '3'),
      run('needs_attention', '2'),
    ])).toBeNull()
  })
})

describe('updateAvailable', () => {
  it('is true only when the newest artifact differs from what runs', () => {
    expect(updateAvailable('v1', 'v2')).toBe(true)
    expect(updateAvailable('v2', 'v2')).toBe(false)
    expect(updateAvailable(undefined, 'v2')).toBe(true)
    expect(updateAvailable('v1', undefined)).toBe(false)
  })
})
