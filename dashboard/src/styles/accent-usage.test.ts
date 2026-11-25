import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, it } from 'vitest'

const SRC = fileURLToPath(new URL('..', import.meta.url))

// the yellow (--primary) is about 1.4:1 on the light page (tokens.test.ts): it is only ever a filled surface carrying
// ink (bg-primary with text-primary-foreground) or a mark on the dark rail (the sidebar-primary tokens), never bare text,
// an icon, a border, an outline, a ring or a stroke
const BARE_YELLOW = /\b(?:text|border|outline|ring|fill|stroke|decoration|divide|caret|placeholder)-primary\b(?!-foreground)/g

function sources(): string[] {
  return readdirSync(SRC, { recursive: true, encoding: 'utf8' })
    .filter(file => /\.tsx?$/.test(file) && !/\.test\.tsx?$/.test(file) && !file.startsWith(join('api', 'gen')))
}

it('never uses the yellow bare on a surface it cannot be read on', () => {
  const found = sources().flatMap(file =>
    [
      ...readFileSync(join(SRC, file), 'utf8').matchAll(BARE_YELLOW),
    ].map(match => `${file}: ${match[0]}`),
  )
  expect(found).toEqual([])
})
