import type { Oklch } from './contrast'
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { contrast, parseOklch } from './contrast'

type Theme = 'light' | 'dark'

const css = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8')
const declared = new Map([
  ...css.matchAll(/^ {2}--([\w-]+): ([^;]+);$/gm),
].map(([, name, value]) => [name!, value!]))

function token(name: string, theme: Theme): Oklch {
  const value = declared.get(name)
  if (value === undefined)
    throw new Error(`no token --${name}`)
  const reference = /^var\(--([\w-]+)\)$/.exec(value)
  if (reference)
    return token(reference[1]!, theme)
  const pair = /^light-dark\((oklch\([^)]*\)),\s*(oklch\([^)]*\))\)$/.exec(value)
  return parseOklch(pair
    ? pair[theme === 'light'
      ? 1
      : 2]!
    : value)
}

// wcag 2.2 aa: 4.5:1 for text (1.4.3), 3:1 for the parts of a control and for graphics (1.4.11)
const TEXT: [string, string][] = [
  ...[
    'background',
    'card',
    'popover',
    'muted',
    'secondary',
    'accent',
  ].map(bg => ['foreground', bg] as [string, string]),
  ...[
    'background',
    'card',
    'muted',
    'accent',
  ].map(bg => ['muted-foreground', bg] as [string, string]),
  ['primary-foreground', 'primary'],
  ['destructive-foreground', 'destructive'],
  ...[
    'destructive',
    'success',
    'warning',
    'info',
  ].flatMap(state => [
    [state, 'background'],
    [state, 'card'],
    [state, `${state}-surface`],
    ['foreground', `${state}-surface`],
    ['muted-foreground', `${state}-surface`],
  ] as [string, string][]),
  ['sidebar-foreground', 'sidebar'],
  ['sidebar-accent-foreground', 'sidebar-accent'],
  ['sidebar-primary', 'sidebar'],
  ['sidebar-primary', 'sidebar-accent'],
  ['sidebar-primary-foreground', 'sidebar-primary'],
]

const UI: [string, string][] = [
  ['input', 'background'],
  ['input', 'card'],
  ['ring', 'background'],
  ['ring', 'card'],
  ['sidebar-ring', 'sidebar'],
  ...[1, 2, 3, 4, 5, 6].flatMap(n => [
    [`chart-${n}`, 'background'],
    [`chart-${n}`, 'card'],
  ] as [string, string][]),
]

describe.each<Theme>(['light', 'dark'])('%s theme', (theme) => {
  it.each(TEXT)('text %s on %s reaches 4.5:1', (fg, bg) => {
    expect(contrast(token(fg, theme), token(bg, theme))).toBeGreaterThanOrEqual(4.5)
  })

  it.each(UI)('%s on %s reaches 3:1', (fg, bg) => {
    expect(contrast(token(fg, theme), token(bg, theme))).toBeGreaterThanOrEqual(3)
  })
})

it('computes the reference ratios', () => {
  expect(contrast(parseOklch('oklch(0 0 0)'), parseOklch('oklch(1 0 0)'))).toBeCloseTo(21, 1)
  expect(contrast(parseOklch('oklch(1 0 0)'), parseOklch('oklch(1 0 0)'))).toBeCloseTo(1, 5)
})

// DESIGN.md (Colors) and accent-usage.test.ts rest on this: on paper the yellow is a surface for ink, never a foreground
it('keeps the yellow at about 1.4:1 on the light page and card', () => {
  expect(contrast(token('primary', 'light'), token('background', 'light'))).toBeCloseTo(1.4, 1)
  expect(contrast(token('primary', 'light'), token('card', 'light'))).toBeLessThan(1.6)
})
