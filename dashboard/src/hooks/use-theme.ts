import type { ThemePreference } from '@/lib/theme'
import { useCallback, useEffect, useSyncExternalStore } from 'react'
import { applyTheme, DARK_QUERY, readPreference, resolveTheme, systemPrefersDark, THEME_STORAGE_KEY, writePreference } from '@/lib/theme'

const changes = new Set<() => void>()

// the root's class is set from what is stored and what the system prefers, read fresh: never from a render, whose
// hydration pass sees the server's snapshot and would flash the other theme
function sync() {
  applyTheme(resolveTheme(readPreference(), systemPrefersDark()))
}

function subscribe(onChange: () => void) {
  changes.add(onChange)
  const media = typeof window.matchMedia === 'function'
    ? window.matchMedia(DARK_QUERY)
    : undefined
  const onSystem = () => {
    sync()
    onChange()
  }
  // another tab's choice
  const onStorage = (event: StorageEvent) => {
    if (event.key === THEME_STORAGE_KEY || event.key === null)
      onSystem()
  }
  media?.addEventListener('change', onSystem)
  window.addEventListener('storage', onStorage)
  return () => {
    changes.delete(onChange)
    media?.removeEventListener('change', onSystem)
    window.removeEventListener('storage', onStorage)
  }
}

// one string, so a stored choice and the system's preference change the snapshot alike
function snapshot() {
  return `${readPreference()}:${systemPrefersDark()
    ? 'dark'
    : 'light'}`
}

export function useTheme() {
  const current = useSyncExternalStore(subscribe, snapshot, () => 'system:light')
  const [preference, system] = current.split(':') as [ThemePreference, 'light' | 'dark']

  useEffect(sync, [])

  const setPreference = useCallback((next: ThemePreference) => {
    writePreference(next)
    sync()
    changes.forEach(onChange => onChange())
  }, [])

  return {
    preference,
    theme: resolveTheme(preference, system === 'dark'),
    setPreference,
  }
}
