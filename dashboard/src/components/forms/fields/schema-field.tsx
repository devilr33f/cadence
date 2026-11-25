import type { FieldNode } from '../json-schema'
import { useFormFields } from '../form-context'
import { BooleanField } from './boolean-field'
import { FieldMessage, RequiredMark } from './field-shell'
import { fieldLabel } from './field-text'
import { RawField } from './raw-field'
import { EnumField, NumberField, StringField } from './scalar-fields'
import { TagsField } from './tags-field'

// an object's fields, one after another; a nested object is a fieldset named by its key, which says what is wrong with
// the object as a whole (left out though required; the server's 422 about a missing or an extra key in it)
export function ObjectFields({ node }: { node: Extract<FieldNode, { kind: 'object' }> }) {
  const form = useFormFields()
  const fields = node.fields.map(field => <SchemaField key={field.pointer} node={field} />)
  if (node.path.length === 0)
    return <div className="flex flex-col gap-5">{fields}</div>
  const id = form.idOf(node.pointer)
  const error = form.errorOf(node.pointer)
  return (
    <fieldset
      className="flex flex-col gap-5 rounded-md border p-4 data-[invalid=true]:border-destructive"
      data-invalid={error ? 'true' : undefined}
      aria-describedby={error
        ? `${id}-error`
        : undefined}
    >
      <legend className={typeof node.schema.title === 'string'
        ? 'px-1 text-sm font-medium'
        : 'px-1 font-mono text-sm font-medium'}
      >
        {fieldLabel(node)}
        <RequiredMark required={node.required} />
      </legend>
      <FieldMessage id={id} error={error} />
      {fields}
    </fieldset>
  )
}

export function SchemaField({ node }: { node: FieldNode }) {
  switch (node.kind) {
    case 'object':
      return <ObjectFields node={node} />
    case 'string':
      return <StringField node={node} />
    case 'enum':
      return <EnumField node={node} />
    case 'integer':
    case 'number':
      return <NumberField node={node} />
    case 'boolean':
      return <BooleanField node={node} />
    case 'tags':
      return <TagsField node={node} />
    case 'raw':
      return <RawField node={node} />
  }
}
