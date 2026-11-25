import type { Ref } from 'react'
import type { ModuleInfo } from '@/api/gen'
import type { SchemaFieldsHandle } from '@/components/forms/schema-fields'
import { FieldMessage } from '@/components/forms/fields/field-shell'
import { SchemaFields } from '@/components/forms/schema-fields'

// the module's config through the form renderer, from the schema the catalog gives it (its fields in the module's
// order); an error about the config as a whole (a path no field has) shows under it. keyed by the module by its
// caller, so nothing typed for one goes to another
export function ProviderConfig({ ref, id, module, initial, error }: {
  ref: Ref<SchemaFieldsHandle>
  id: string
  module: ModuleInfo
  initial: Record<string, unknown>
  error?: string
}) {
  return (
    <fieldset
      className="flex flex-col gap-4"
      data-invalid={error ? true : undefined}
      aria-describedby={error
        ? `${id}-error`
        : undefined}
    >
      <legend className="mb-1 text-sm font-medium">Configuration</legend>
      <SchemaFields
        ref={ref}
        schema={module.configSchema}
        initial={initial}
        label="Configuration"
        empty={<p className="text-sm text-muted-foreground">This module takes no configuration.</p>}
      />
      <FieldMessage id={id} error={error} />
    </fieldset>
  )
}
