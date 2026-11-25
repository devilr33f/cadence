import type { ReactNode, Ref } from 'react'
import type { FormFields } from './form-context'
import type { JsonObject } from './json-pointer'
import type { FieldNode } from './json-schema'
import { useId, useImperativeHandle, useMemo, useState } from 'react'
import { RawDocument } from './fields/raw-field'
import { ObjectFields } from './fields/schema-field'
import { FormFieldsContext } from './form-context'
import { pointerOf, withValueAt } from './json-pointer'
import { buildForm, fieldFor } from './json-schema'
import { withRequiredSwitches } from './required-switches'
import { validateForm } from './validate'

// what the form around the fields asks of them
export interface SchemaFieldsHandle {
  // a submit was tried, so their errors show from now on: the value to send, or undefined while a field holds it back
  submit: () => JsonObject | undefined
  // the server's refusal on the field its pointer (relative to this document) leads into: false when none has it
  refuse: (pointer: string, message: string) => boolean
  // the server took the value: errors wait for the next try
  settle: () => void
}

type Root = Extract<FieldNode, { kind: 'object' | 'raw' }>

const WHOLE: Root = {
  kind: 'raw',
  path: [],
  pointer: '',
  name: '',
  required: true,
  schema: {},
}

// the document is an object (a config, a command's arguments): a schema whose root is anything else is edited as json
function rootOf(schema: unknown, initial: JsonObject): Root {
  if (schema === undefined)
    return WHOLE
  const built = buildForm(schema, initial)
  return built.kind === 'object' || built.kind === 'raw'
    ? built
    : { ...WHOLE, schema: built.schema }
}

function domId(base: string, pointer: string): string {
  return `${base}-f${pointer.replace(/[^\w-]/g, c => `_${c.charCodeAt(0)}`)}`
}

// a value edited through the schema (json-schema.ts names the subset) or as raw json, inside a form of the caller's; the
// schema is read once, from the value the fields open with, so a part shown as json stays json while it is typed
export function SchemaFields({ ref, schema, initial, label, empty }: {
  ref: Ref<SchemaFieldsHandle>
  // undefined while the server sends no schema for it: the whole value is edited as json
  schema?: unknown
  initial: JsonObject
  // the json editor's label, when the whole value is one
  label: string
  // what an object without fields says (a command without arguments)
  empty?: ReactNode
}) {
  const baseId = useId()
  const [node] = useState(() => rootOf(schema, initial))
  const [value, setValue] = useState<unknown>(initial)
  const [local, setLocal] = useState<Record<string, string | undefined>>({})
  const [server, setServer] = useState<Record<string, string>>({})
  const [tried, setTried] = useState(false)

  // what the form would send: the value with each untouched required switch as the off it shows
  const body = useMemo(() => withRequiredSwitches(node, value), [node, value])
  const client = useMemo(() => validateForm(node, body), [node, body])

  useImperativeHandle(ref, () => ({
    submit() {
      setTried(true)
      if (Object.values(local).some(Boolean) || client.size > 0)
        return undefined
      setServer({})
      return body as JsonObject
    },
    refuse(pointer, message) {
      const field = fieldFor(node, pointer)
      if (!field)
        return false
      setServer({ [field.pointer]: message })
      return true
    },
    settle: () => setTried(false),
  }))

  const fields: FormFields = {
    root: value,
    set(path, next) {
      setValue((current: unknown) => withValueAt(current, path, next))
      const pointer = pointerOf(path)
      setServer(({ [pointer]: _, ...rest }) => rest)
    },
    errorOf: pointer => server[pointer] ?? (tried
      ? local[pointer] ?? client.get(pointer)
      : undefined),
    reportLocal: (pointer, error) => setLocal(current => ({ ...current, [pointer]: error })),
    idOf: pointer => domId(baseId, pointer),
  }

  return (
    <FormFieldsContext value={fields}>
      {node.kind === 'object' && node.fields.length === 0 && (empty ?? <p className="text-sm text-muted-foreground">Nothing to set.</p>)}
      {node.kind === 'object' && <ObjectFields node={node} />}
      {node.kind !== 'object' && (
        <RawDocument
          node={node}
          label={`${label} (JSON)`}
          note={schema === undefined
            ? undefined
            : 'Edited as JSON: the form cannot show this schema.'}
        />
      )}
    </FormFieldsContext>
  )
}
