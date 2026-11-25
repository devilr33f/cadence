import type { RefObject } from 'react'
import type { ProviderField } from './provider-refusal'
import type { Instance, ModuleSecret } from '@/api/gen'
import { FieldMessage } from '@/components/forms/fields/field-shell'
import { describedBy } from '@/components/forms/fields/field-text'
import { FormField } from '@/components/forms/form-field'
import { SecretInput } from '@/components/forms/secret-input'
import { Button } from '@/components/ui/button'
import { secretField } from './provider-refusal'
import { SecretFileLoader } from './secret-file'
import { clearFileText, FILE_SECRETS } from './secret-file-data'

const OPTIONAL = 'Optional.'

// one write-only field per secret of the module, uncontrolled: the form reads the inputs through `inputs` at submit
// and clears them at once. an edit gets set states alone, never values; empty keeps, clear removes an optional one
export function ProviderSecretFields({ baseId, fields, states, cleared, inputs, errors, onChange, onClear }: {
  baseId: string
  fields: readonly ModuleSecret[]
  states?: Instance['secrets']
  cleared: ReadonlySet<string>
  inputs: RefObject<Map<string, HTMLInputElement>>
  errors: Partial<Record<ProviderField, string>>
  onChange: (field: ProviderField) => void
  onClear: (name: string, clear: boolean) => void
}) {
  if (fields.length === 0)
    return null
  const groupId = `${baseId}-secrets`
  const groupError = errors.secrets
  return (
    <fieldset
      className="flex flex-col gap-4"
      data-invalid={groupError ? true : undefined}
      aria-describedby={describedBy(groupId, { description: true, error: groupError })}
    >
      <legend className="mb-1 text-sm font-medium">Secrets</legend>
      <p id={`${groupId}-description`} className="-mt-2 text-sm text-muted-foreground">
        {states
          ? 'Write-only: leave a set secret empty to keep it, or enter a new value to replace it. Values are cleared from the form after each try.'
          : 'Write-only: sent with this request and cleared from the form after each try. The list shows only whether each is set.'}
      </p>
      {fields.map(({ name, required }) => {
        const key = secretField(name)
        const id = `${baseId}-secret-${name}`
        const error = errors[key]
        const set = states?.[name]?.set === true
        const isCleared = cleared.has(name)
        const requiredNow = required && !set
        let description = required ? undefined : OPTIONAL
        if (states) {
          description = isCleared
            ? 'Will be cleared when you save.'
            : set ? 'Set. Leave empty to keep it.' : 'Not set. Leave empty to keep it unset.'
        }
        return (
          <FormField key={name} id={id} label={name} mono required={requiredNow} description={description} error={error}>
            <SecretInput
              ref={(input) => {
                if (input)
                  inputs.current.set(name, input)
                return () => {
                  inputs.current.delete(name)
                }
              }}
              id={id}
              disabled={isCleared}
              aria-required={requiredNow || undefined}
              aria-invalid={error ? true : undefined}
              aria-describedby={describedBy(id, { description: description !== undefined, error })}
              onChange={() => {
                const input = inputs.current.get(name)
                if (input)
                  clearFileText(input)
                onChange(key)
              }}
            />
            <div className="flex flex-wrap items-center gap-2">
              {FILE_SECRETS.has(name) && !isCleared && (
                <SecretFileLoader id={id} target={() => inputs.current.get(name)} onLoaded={() => onChange(key)} />
              )}
              {states && !required && set && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={`${isCleared ? 'Undo clear' : 'Clear'} ${name}`}
                  onClick={() => {
                    const input = inputs.current.get(name)
                    if (input) {
                      clearFileText(input)
                      input.value = ''
                    }
                    onClear(name, !isCleared)
                  }}
                >
                  {isCleared ? 'Undo clear' : 'Clear'}
                </Button>
              )}
            </div>
          </FormField>
        )
      })}
      <FieldMessage id={groupId} error={groupError} />
    </fieldset>
  )
}
