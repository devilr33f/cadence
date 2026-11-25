import { readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = join(import.meta.dirname, '..')
const FORM = /<form\b(?<rest>[^\n]*)/g
const POSTS = /^ method="post"(?:\s|>)/

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      return entry.name === 'gen'
        ? []
        : sources(path)
    }
    return /\.tsx$/.test(entry.name) && !/\.test\.tsx$/.test(entry.name)
      ? [path]
      : []
  })
}

interface Form {
  file: string
  posts: boolean
}

function formsOf(file: string): Form[] {
  const name = relative(SRC, file).replaceAll('\\', '/')
  return [
    ...readFileSync(file, 'utf8').matchAll(FORM),
  ].map(match => ({
    file: name,
    posts: POSTS.test(match.groups!.rest!),
  }))
}

// a form the browser submits itself (before hydration, or with scripts off) sends its named fields: a get puts a
// password, a secret or a reply in the url, a post goes in a body the image's server never reads (405). so every form
// says method="post" first, where this guard can see it
describe('every form of the dashboard', () => {
  it('posts', () => {
    const files = sources(SRC)
    expect(files.length).toBeGreaterThan(50)
    const forms = files.flatMap(formsOf)
    expect(forms.length).toBeGreaterThan(10)
    expect(forms.filter(f => !f.posts).map(f => f.file)).toEqual([])
  })
})
