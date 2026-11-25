// @vitest-environment jsdom
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { THEME_STORAGE_KEY } from '@/lib/theme'
import { stubColorScheme } from '@/test/media'
import { setupUser } from '@/test/typing'
import { ThemeToggle } from './theme-toggle'

const root = document.documentElement

beforeEach(() => {
  localStorage.clear()
  root.className = ''
})

afterEach(cleanup)

async function choose(name: string) {
  const user = setupUser()
  await user.click(screen.getByRole('button', { name: /theme/i }))
  await user.click(await screen.findByRole('menuitemradio', { name }))
}

it('follows the system preference until a choice is made', () => {
  stubColorScheme(true)
  render(<ThemeToggle />)
  expect(root.classList.contains('dark')).toBe(true)
  expect(screen.getByRole('button', { name: 'Theme: system (dark)' })).toBeTruthy()
  expect(localStorage.getItem(THEME_STORAGE_KEY)).toBeNull()
})

it('persists a choice and applies it at once', async () => {
  stubColorScheme(true)
  render(<ThemeToggle />)
  await choose('Light')
  expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe('light')
  expect(root.classList.contains('light')).toBe(true)
  expect(root.classList.contains('dark')).toBe(false)

  cleanup()
  render(<ThemeToggle />)
  expect(screen.getByRole('button', { name: 'Theme: light' })).toBeTruthy()
})

it('forgets the stored choice when set back to the system', async () => {
  stubColorScheme(false)
  localStorage.setItem(THEME_STORAGE_KEY, 'dark')
  render(<ThemeToggle />)
  expect(root.classList.contains('dark')).toBe(true)

  await choose('System')
  expect(localStorage.getItem(THEME_STORAGE_KEY)).toBeNull()
  expect(root.classList.contains('light')).toBe(true)
})

it('follows a change of the system preference while on system', () => {
  const scheme = stubColorScheme(false)
  render(<ThemeToggle />)
  expect(root.classList.contains('light')).toBe(true)

  act(() => scheme.change(true))
  expect(root.classList.contains('dark')).toBe(true)
})
