import type { DurationValues } from './alarm-form'
import { FieldMessage, RequiredMark } from '@/components/forms/fields/field-shell'
import { describedBy } from '@/components/forms/fields/field-text'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

const PARTS = [
  { key: 'hours', label: 'Hours' },
  { key: 'minutes', label: 'Minutes' },
  { key: 'seconds', label: 'Seconds' },
] as const

// one duration as three owner-readable fields; validation checks their total against the command's seconds bound
export function DurationFields({ id, values, description, error, onChange }: {
  id: string
  values: DurationValues
  description: string
  error?: string
  onChange: (values: DurationValues) => void
}) {
  return (
    <fieldset
      className="flex flex-col gap-2"
      aria-invalid={error ? true : undefined}
      aria-describedby={describedBy(id, { description: true, error })}
    >
      <legend className="text-sm font-medium">
        Duration
        <RequiredMark required />
      </legend>
      <p id={`${id}-description`} className="text-sm text-muted-foreground">{description}</p>
      <div className="grid grid-cols-3 gap-3">
        {PARTS.map(part => (
          <div key={part.key} className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-${part.key}`}>{part.label}</Label>
            <Input
              id={`${id}-${part.key}`}
              inputMode="numeric"
              autoComplete="off"
              value={values[part.key]}
              onChange={event => onChange({ ...values, [part.key]: event.target.value })}
            />
          </div>
        ))}
      </div>
      <FieldMessage id={id} error={error} />
    </fieldset>
  )
}
