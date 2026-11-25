// a prefers-color-scheme the test controls (jsdom has no matchMedia)
export function stubColorScheme(dark: boolean) {
  const listeners = new Set<() => void>()
  const query = {
    get matches() {
      return dark
    },
    media: '(prefers-color-scheme: dark)',
    addEventListener: (_: string, listener: () => void) => listeners.add(listener),
    removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
  }
  window.matchMedia = (() => query) as unknown as typeof window.matchMedia
  return {
    change(next: boolean) {
      dark = next
      listeners.forEach(listener => listener())
    },
  }
}
