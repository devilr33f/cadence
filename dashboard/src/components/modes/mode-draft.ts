import type { ModeView, PutModeRequest } from '@/api/gen'
import { parseNumber } from '@/components/forms/parse-number'

export type Reasoning = ModeView['reasoning']
export type Interim = ModeView['interim']

export const REASONING: readonly Reasoning[] = ['none', 'low', 'medium', 'high']
export const INTERIM: readonly Interim[] = ['drop', 'speak']

// a mode as the form edits it: the budgets as typed (maxTokens and firstTokenSeconds empty keep the built-in defaults;
// the server takes the first-token timeout in milliseconds), the tools apart from
// the switch that offers every one, so turning it off and on again keeps the list
export interface ModeDraft {
  name: string
  model: string
  reasoning: Reasoning
  maxSteps: string
  maxSeconds: string
  maxTokens: string
  firstTokenSeconds: string
  interim: Interim
  allTools: boolean
  tools: string[]
  en: string[]
  ru: string[]
}

export type ModeField = 'name' | 'model' | 'reasoning' | 'maxSteps' | 'maxSeconds' | 'maxTokens' | 'firstTokenSeconds' | 'interim' | 'tools' | 'en' | 'ru'

export type ModeErrors = Partial<Record<ModeField, string>>

// a new mode starts as fast's budgets with the device's model and every tool
export const NEW_MODE: ModeDraft = {
  name: '',
  model: '',
  reasoning: 'none',
  maxSteps: '8',
  maxSeconds: '20',
  maxTokens: '',
  firstTokenSeconds: '',
  interim: 'drop',
  allTools: true,
  tools: [],
  en: [],
  ru: [],
}

export function draftOf(mode: ModeView): ModeDraft {
  return {
    name: mode.name,
    model: mode.model,
    reasoning: mode.reasoning,
    maxSteps: String(mode.maxSteps),
    maxSeconds: String(mode.maxSeconds),
    maxTokens: String(mode.maxTokens ?? ''),
    firstTokenSeconds: mode.firstTokenTimeoutMs
      ? String(mode.firstTokenTimeoutMs / 1000)
      : '',
    interim: mode.interim,
    allTools: mode.tools === null,
    tools: mode.tools ?? [],
    en: mode.aliases.en ?? [],
    ru: mode.aliases.ru ?? [],
  }
}

// the whole mode, as a put replaces it: null offers every tool, an empty list none; a language without aliases is left
// out, and an empty token ceiling or first-token timeout keeps the built-in default
export function requestOf(draft: ModeDraft): PutModeRequest {
  return {
    model: draft.model,
    reasoning: draft.reasoning,
    maxSteps: Number(draft.maxSteps.trim()),
    maxSeconds: Number(draft.maxSeconds.trim()),
    ...(draft.maxTokens.trim() !== '' && {
      maxTokens: Number(draft.maxTokens.trim()),
    }),
    ...(draft.firstTokenSeconds.trim() !== '' && {
      firstTokenTimeoutMs: Number(draft.firstTokenSeconds.trim()) * 1000,
    }),
    interim: draft.interim,
    tools: draft.allTools
      ? null
      : draft.tools,
    aliases: {
      ...(draft.en.length > 0 && { en: draft.en }),
      ...(draft.ru.length > 0 && { ru: draft.ru }),
    },
  }
}

// the server's rules (server/internal/modes), which it checks again
const NAME = /^[a-z][a-z0-9_-]{0,31}$/
const TOOL = /^[\w-]{1,64}$/
const HIDDEN = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u
const MAX_MODEL_BYTES = 200
const MAX_TOOLS = 64
const MAX_ALIASES = 10
const MAX_ALIAS_CHARS = 40
const MIN_TOKENS = 64
const MAX_TOKENS = 32768
const MIN_FIRST_TOKEN_SECONDS = 2
const MAX_FIRST_TOKEN_SECONDS = 120

const encoder = new TextEncoder()

function budgetError(text: string, min: number, max: number): string | undefined {
  const parsed = parseNumber(text, 'integer')
  if (parsed.error)
    return parsed.error
  if (parsed.value === undefined)
    return 'Required.'
  if (parsed.value < min)
    return `At least ${min}.`
  if (parsed.value > max)
    return `At most ${max}.`
  return undefined
}

// an optional budget: empty keeps the default, so it is never required
function optionalBudgetError(text: string, min: number, max: number): string | undefined {
  return text.trim() === ''
    ? undefined
    : budgetError(text, min, max)
}

// what the server would refuse before it is sent; `taken` are the names a create may not reuse (a put would replace)
export function checkDraft(draft: ModeDraft, creating: boolean, taken: readonly string[]): ModeErrors {
  const found: ModeErrors = {}
  if (creating) {
    if (draft.name === '')
      found.name = 'Required.'
    else if (!NAME.test(draft.name))
      found.name = 'Lowercase letters, digits, "_" or "-", starting with a letter; at most 32.'
    else if (taken.includes(draft.name))
      found.name = 'A mode has this name: edit it instead.'
  }
  if (encoder.encode(draft.model).length > MAX_MODEL_BYTES)
    found.model = `At most ${MAX_MODEL_BYTES} bytes.`
  else if (HIDDEN.test(draft.model))
    found.model = 'No control or format characters.'
  const steps = budgetError(draft.maxSteps, 1, 50)
  if (steps)
    found.maxSteps = steps
  const seconds = budgetError(draft.maxSeconds, 5, 600)
  if (seconds)
    found.maxSeconds = seconds
  const tokens = optionalBudgetError(draft.maxTokens, MIN_TOKENS, MAX_TOKENS)
  if (tokens)
    found.maxTokens = tokens
  const firstToken = optionalBudgetError(draft.firstTokenSeconds, MIN_FIRST_TOKEN_SECONDS, MAX_FIRST_TOKEN_SECONDS)
  if (firstToken)
    found.firstTokenSeconds = firstToken
  return found
}

// a tool to add to the list, or why not
export function toolError(item: string, items: readonly string[]): string | undefined {
  if (!TOOL.test(item))
    return 'Letters, digits, "_" or "-"; at most 64.'
  if (items.includes(item))
    return 'Already in the list.'
  if (items.length >= MAX_TOOLS)
    return `At most ${MAX_TOOLS} tools.`
  return undefined
}

// an alias to add (already trimmed), or why not: distinct in any case, as the agent_mode tool matches them
export function aliasError(item: string, items: readonly string[]): string | undefined {
  if ([...item].length > MAX_ALIAS_CHARS)
    return `At most ${MAX_ALIAS_CHARS} characters.`
  if (HIDDEN.test(item))
    return 'No control or format characters.'
  if (items.some(i => i.toLowerCase() === item.toLowerCase()))
    return 'Already in the list.'
  if (items.length >= MAX_ALIASES)
    return `At most ${MAX_ALIASES} aliases.`
  return undefined
}
