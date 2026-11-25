// a test's console.error fails it: react says what is wrong there (a duplicate key, an update outside act, invalid dom
// nesting), and vitest hides a passing test's output. a test that expects one says so by mocking console.error itself
// (vi.spyOn(console, 'error').mockImplementation(...)), which takes the calls before the guard sees them

// wraps console.error for one test; the check puts it back and throws when anything reached it
export function guardConsoleErrors(): () => void {
  const original = console.error
  const calls: unknown[][] = []
  console.error = (...args: unknown[]) => {
    calls.push(args)
    original(...args)
  }
  return () => {
    console.error = original
    if (calls.length === 0)
      return
    const first = calls[0]!.map(a => a instanceof Error
      ? a.message
      : String(a)).join(' ')
    throw new Error(`console.error was called ${calls.length} time(s); mock it in a test that expects it. first: ${first.slice(0, 300)}`)
  }
}
