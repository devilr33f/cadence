import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { segmentsOf } from '@/components/forms/json-pointer'
import { refusedText } from '@/components/forms/refusal'

// the create form's fields a refusal can land on; a secret's is `secret:<name>`
export type ProviderField = 'name' | 'module' | 'config' | 'secrets' | `secret:${string}`

export interface ProviderRefusal {
  field?: ProviderField
  message: string
  // a refusal of the config: where in it, for the config's own field when one has it, and its words there
  config?: { pointer: string, message: string }
}

export function secretField(name: string): ProviderField {
  return `secret:${name}`
}

// where the server's answer belongs: its 422 config_invalid points at the name (/name), a secret (/secrets/<name> or
// /clearSecrets/<name>), the secrets as a whole or else into the config, its pointer relative to the config (no module's
// config has a name or a secrets key); a taken name and an unknown module are the name's and the module's. anything else
// is the form's, in codes
export function providerRefusal(error: unknown, secrets: readonly string[]): ProviderRefusal {
  if (!(error instanceof ApiError)) {
    return {
      message: describeError(error),
    }
  }
  if (error.error === 'name_taken') {
    return {
      field: 'name',
      message: describeError(error),
    }
  }
  if (error.error === 'unknown_module') {
    return {
      field: 'module',
      message: describeError(error),
    }
  }
  if (error.status !== 422 || error.path === undefined) {
    return {
      message: describeError(error),
    }
  }

  const refused = refusedText(error)
  const [first, ...rest] = segmentsOf(error.path)
  if (first === 'name' && rest.length === 0)
    return { field: 'name', message: refused }
  if (first === 'secrets' || first === 'clearSecrets') {
    const name = rest[0]
    if (rest.length === 1 && name !== undefined && secrets.includes(name)) {
      return {
        field: secretField(name),
        message: refused,
      }
    }
    return secrets.length > 0
      ? { field: 'secrets', message: `The server refused these secrets (${error.error}).` }
      : {
          message: describeError(error),
        }
  }
  return {
    field: 'config',
    message: error.path === ''
      ? `The server refused the configuration (${error.error}).`
      : `The server refused the configuration at ${error.path} (${error.error}).`,
    config: { pointer: error.path, message: refused },
  }
}
