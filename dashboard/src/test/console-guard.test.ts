import { describe, expect, it, vi } from 'vitest'
import { guardConsoleErrors } from './console-guard'

describe('the console.error guard', () => {
  it('fails a test that logged a react warning, naming it', () => {
    // what the guard forwards to, silent here
    const shown = vi.spyOn(console, 'error').mockImplementation(() => {})
    const check = guardConsoleErrors()
    console.error('Encountered two children with the same key, `%s`.', 'kitchen')
    expect(check).toThrow(/^console\.error was called 1 time\(s\); .* first: Encountered two children with the same key/)
    expect(shown).toHaveBeenCalledOnce()
    expect(console.error).toBe(shown)
    shown.mockRestore()
  })

  it('lets a test that mocks console.error expect one', () => {
    const check = guardConsoleErrors()
    const expected = vi.spyOn(console, 'error').mockImplementation(() => {})
    console.error('The above error occurred in the <Route> component.')
    expect(expected).toHaveBeenCalledOnce()
    expected.mockRestore()
    expect(check).not.toThrow()
  })

  it('passes a test that logged nothing', () => {
    expect(guardConsoleErrors()).not.toThrow()
  })
})
