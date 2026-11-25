import type { FieldNode } from '../json-schema'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { useFormFields } from '../form-context'
import { valueAt } from '../json-pointer'
import { FieldShell } from './field-shell'
import { fieldDescribedBy, fieldLabel } from './field-text'

// a switch has no "unset": an optional key left out shows the schema's default (else off) and says so, and one that is
// set can be cleared back to the device's own default; a required one left out is off, and the form sends it so
export function BooleanField({ node }: { node: Extract<FieldNode, { kind: 'boolean' }> }) {
  const form = useFormFields()
  const id = form.idOf(node.pointer)
  const error = form.errorOf(node.pointer)
  const value = valueAt(form.root, node.path)
  const set = typeof value === 'boolean'
  const shown = set
    ? value
    : !node.required && node.schema.default === true
  return (
    <FieldShell
      node={node}
      id={id}
      labelId={`${id}-label`}
      error={error}
      action={set && !node.required && (
        <Button type="button" variant="ghost" size="xs" onClick={() => form.set(node.path, undefined)}>
          {`Clear ${fieldLabel(node)}`}
        </Button>
      )}
    >
      <div className="flex items-center gap-3">
        <Switch
          checked={shown}
          aria-labelledby={`${id}-label`}
          aria-invalid={error ? true : undefined}
          aria-describedby={fieldDescribedBy(node, id, error)}
          onCheckedChange={checked => form.set(node.path, checked)}
        />
        <span className="text-sm text-muted-foreground">
          {set || node.required
            ? (shown ? 'On' : 'Off')
            : 'Not set: the device decides'}
        </span>
      </div>
    </FieldShell>
  )
}
