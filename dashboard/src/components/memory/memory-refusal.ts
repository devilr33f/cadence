import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'

export type MemoryField = 'text' | 'kind' | 'scope' | 'form'

// the memory routes name the wrong field bare (`text`); a pointer (`/text`, huma's `/body/text`) is read the same.
// a device that is gone is the scope's to say
const FIELDS: Readonly<Record<string, MemoryField>> = {
  text: 'text',
  kind: 'kind',
  scope: 'scope',
  deviceId: 'scope',
}

export function memoryRefusal(error: unknown): { field: MemoryField, message: string } {
  if (error instanceof ApiError && error.status === 422 && error.path !== undefined) {
    const field = FIELDS[error.path.replace(/^\/?(?:body\/)?/, '')]
    if (field)
      return { field, message: `The server refused this value (${error.error}).` }
  }
  return {
    field: 'form',
    message: describeError(error),
  }
}
