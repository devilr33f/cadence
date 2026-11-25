import type { PutSkillRequest, SkillView } from '@/api/gen'

// a skill as the form edits it: the modes apart from the switch that lists it in every mode, so turning it off and on
// again keeps the list
export interface SkillDraft {
  name: string
  description: string
  body: string
  enabled: boolean
  triggers: string[]
  tools: string[]
  allModes: boolean
  modes: string[]
}

export type SkillField = 'name' | 'description' | 'body' | 'triggers' | 'tools' | 'modes'

export type SkillErrors = Partial<Record<SkillField, string>>

export const NEW_SKILL: SkillDraft = {
  name: '',
  description: '',
  body: '',
  enabled: true,
  triggers: [],
  tools: [],
  allModes: true,
  modes: [],
}

export function draftOf(skill: SkillView): SkillDraft {
  return {
    name: skill.name,
    description: skill.description,
    body: skill.body,
    enabled: skill.enabled,
    triggers: skill.triggers,
    tools: skill.tools,
    allModes: skill.modes === null,
    modes: skill.modes ?? [],
  }
}

// the whole skill, as a put stores it: null modes lists it in every mode
export function requestOf(draft: SkillDraft): PutSkillRequest {
  return {
    description: draft.description.trim(),
    body: draft.body.trim(),
    enabled: draft.enabled,
    triggers: draft.triggers,
    tools: draft.tools,
    modes: draft.allModes
      ? null
      : draft.modes,
  }
}

// the server's rules (server/internal/skills), which it checks again
const NAME = /^[a-z][a-z0-9_-]{0,47}$/
const ID = /^[\w-]{1,64}$/
const HIDDEN = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u
const WORD = /[\p{L}\p{N}]/u
const MAX_DESCRIPTION = 200
const MAX_BODY = 4000
const MAX_TRIGGERS = 48
const MAX_TRIGGER = 64
const MAX_TOOLS = 24
const MAX_MODES = 16

const length = (s: string) => [...s].length

// what the server would refuse before it is sent; `taken` are the names a create may not reuse (a put would replace)
export function checkDraft(draft: SkillDraft, creating: boolean, taken: readonly string[]): SkillErrors {
  const found: SkillErrors = {}
  if (creating) {
    if (draft.name === '')
      found.name = 'Required.'
    else if (!NAME.test(draft.name))
      found.name = 'Lowercase letters, digits, "_" or "-", starting with a letter; at most 48.'
    else if (taken.includes(draft.name))
      found.name = 'A skill has this name: edit it instead.'
  }
  const description = draft.description.trim()
  if (description === '')
    found.description = 'Required.'
  else if (length(description) > MAX_DESCRIPTION)
    found.description = `At most ${MAX_DESCRIPTION} characters.`
  else if (HIDDEN.test(description))
    found.description = 'One line, no control or format characters.'
  const body = draft.body.trim()
  if (body === '')
    found.body = 'Required.'
  else if (length(body) > MAX_BODY)
    found.body = `At most ${MAX_BODY} characters.`
  return found
}

function idError(max: number, what: string) {
  return (item: string, items: readonly string[]): string | undefined => {
    if (!ID.test(item))
      return 'Letters, digits, "_" or "-"; at most 64.'
    if (items.includes(item))
      return 'Already in the list.'
    if (items.length >= max)
      return `At most ${max} ${what}.`
    return undefined
  }
}

export const toolError = idError(MAX_TOOLS, 'tools')
export const modeError = idError(MAX_MODES, 'modes')

// a trigger to add (already trimmed), or why not: a word or phrase matched in any case
export function triggerError(item: string, items: readonly string[]): string | undefined {
  if (length(item) > MAX_TRIGGER)
    return `At most ${MAX_TRIGGER} characters.`
  if (!WORD.test(item))
    return 'A word or phrase, with a letter or digit.'
  if (HIDDEN.test(item))
    return 'No control or format characters.'
  if (items.some(i => i.toLowerCase() === item.toLowerCase()))
    return 'Already in the list.'
  if (items.length >= MAX_TRIGGERS)
    return `At most ${MAX_TRIGGERS} triggers.`
  return undefined
}

// where a put's refusal belongs: 422 invalid names the field (/body/<field>[/<i>], /path/name); anything else goes
// beside the form
export function fieldOf(path: string | undefined): SkillField | undefined {
  const m = path?.match(/^\/(body|path)\/(\w+)/)
  if (!m)
    return undefined
  const key = m[2]
  if (m[1] === 'path') {
    return key === 'name'
      ? 'name'
      : undefined
  }
  return ([
    'description',
    'body',
    'triggers',
    'tools',
    'modes',
  ] as const).find(f => f === key)
}
