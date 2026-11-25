import type { ModeDraft, ModeField } from './mode-draft'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { segmentsOf } from '@/components/forms/json-pointer'
import { refusedText } from '@/components/forms/refusal'

export interface ModeRefusal {
  field?: ModeField
  message: string
}

const BODY_FIELDS: ReadonlySet<string> = new Set([
  'model',
  'reasoning',
  'maxSteps',
  'maxSeconds',
  'maxTokens',
  'interim',
])

// a body field the form edits under another name: the first-token timeout is typed in seconds
const RENAMED: Readonly<Record<string, ModeField>> = {
  firstTokenTimeoutMs: 'firstTokenSeconds',
}

function itemAt(list: readonly string[], index: string | undefined): string | undefined {
  return index !== undefined && /^\d+$/.test(index)
    ? list[Number(index)]
    : undefined
}

// where a put's refusal belongs: 409 alias_taken names the alias (/body/aliases/<lang>/<i>) or the new mode's name
// (/path/name), which another mode has as an alias; 422 invalid names the field (huma's /body/<field>, /path/name). an
// item is named by what was typed, the owner's own words; anything else goes beside the form, in codes
export function modeRefusal(error: unknown, draft: ModeDraft, creating: boolean): ModeRefusal {
  if (!(error instanceof ApiError) || error.path === undefined) {
    return {
      message: describeError(error),
    }
  }
  const taken = error.error === 'alias_taken'
  const [where, key, index, ...rest] = segmentsOf(error.path)
  if (where === 'path' && key === 'name' && index === undefined && creating) {
    return {
      field: 'name',
      message: taken
        ? `Another mode has this as an alias (${error.error}).`
        : refusedText(error),
    }
  }
  if (where === 'body' && key !== undefined && rest.length === 0) {
    if (RENAMED[key] && index === undefined) {
      return {
        field: RENAMED[key],
        message: refusedText(error),
      }
    }
    if (BODY_FIELDS.has(key) && index === undefined) {
      return {
        field: key as ModeField,
        message: refusedText(error),
      }
    }
    if (key === 'tools' && !draft.allTools) {
      const item = itemAt(draft.tools, index)
      return {
        field: 'tools',
        message: item === undefined
          ? refusedText(error)
          : `The server refused «${item}» (${error.error}).`,
      }
    }
  }
  if (where === 'body' && key === 'aliases' && (index === 'en' || index === 'ru') && rest.length <= 1) {
    const item = itemAt(draft[index], rest[0])
    let message = refusedText(error)
    if (item !== undefined) {
      message = taken
        ? `«${item}» is another mode's name or alias (${error.error}).`
        : `The server refused «${item}» (${error.error}).`
    }
    return { field: index, message }
  }
  return { message: `The server answered ${error.status} ${error.error} at ${error.path}.` }
}
