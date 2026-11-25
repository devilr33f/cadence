// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { stubColorScheme } from '@/test/media'
import { THEME_SCRIPT, THEME_STORAGE_KEY } from './theme'

const root = document.documentElement

function runScript() {
  // eslint-disable-next-line no-new-func
  new Function(THEME_SCRIPT)()
}

describe('the theme script in the document head', () => {
  beforeEach(() => {
    localStorage.clear()
    root.className = ''
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('follows the system preference when nothing is stored', () => {
    stubColorScheme(true)
    runScript()
    expect(root.className).toBe('dark')

    stubColorScheme(false)
    runScript()
    expect(root.className).toBe('light')
  })

  it('lets a stored choice win over the system', () => {
    stubColorScheme(true)
    localStorage.setItem(THEME_STORAGE_KEY, 'light')
    runScript()
    expect(root.className).toBe('light')
  })

  it('ignores a stored value it does not know', () => {
    stubColorScheme(true)
    localStorage.setItem(THEME_STORAGE_KEY, 'sepia')
    runScript()
    expect(root.className).toBe('dark')
  })

  it('falls back to the system when storage is blocked', () => {
    stubColorScheme(false)
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    runScript()
    expect(root.className).toBe('light')
  })
})
