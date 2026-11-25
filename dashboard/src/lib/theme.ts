export type ThemePreference = 'system' | 'light' | 'dark'
export type Theme = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'cadence-theme'
export const DARK_QUERY = '(prefers-color-scheme: dark)'

// where storage is blocked, a choice lasts until the page closes
let unstored: ThemePreference = 'system'

export function readPreference(): ThemePreference {
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY)
    return stored === 'light' || stored === 'dark'
      ? stored
      : 'system'
  }
  catch {
    return unstored
  }
}

export function writePreference(preference: ThemePreference) {
  try {
    if (preference === 'system')
      localStorage.removeItem(THEME_STORAGE_KEY)
    else
      localStorage.setItem(THEME_STORAGE_KEY, preference)
  }
  catch {
    unstored = preference
  }
}

export function systemPrefersDark(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia(DARK_QUERY).matches
}

export function resolveTheme(preference: ThemePreference, prefersDark: boolean): Theme {
  if (preference === 'system')
    return prefersDark ? 'dark' : 'light'
  return preference
}

export function applyTheme(theme: Theme) {
  const { classList } = document.documentElement
  classList.remove(theme === 'dark'
    ? 'light'
    : 'dark')
  classList.add(theme)
}

// the same steps as a standalone script in the document head, so the first paint already has the reader's theme
function boot(key: string, query: string) {
  let stored: string | null = null
  try {
    stored = localStorage.getItem(key)
  }
  catch {}
  const dark = stored === 'dark' || (stored !== 'light' && typeof matchMedia === 'function' && matchMedia(query).matches)
  const classList = document.documentElement.classList
  classList.remove(dark ? 'light' : 'dark')
  classList.add(dark ? 'dark' : 'light')
}

export const THEME_SCRIPT = `(${boot.toString()})(${JSON.stringify(THEME_STORAGE_KEY)},${JSON.stringify(DARK_QUERY)})`
