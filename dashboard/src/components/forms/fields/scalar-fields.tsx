import type { FieldNode } from '../json-schema'
import { useState } from 'react'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { useFormFields } from '../form-context'
import { valueAt } from '../json-pointer'
import { parseNumber } from '../parse-number'
import { FieldShell } from './field-shell'
import { defaultHint, fieldDescribedBy } from './field-text'

type Node<K extends FieldNode['kind']> = Extract<FieldNode, { kind: K }>

// an empty field is no value: the key is left out; `format: 'multiline'` (an annotation, as json schema leaves
// unknown formats) asks for a box that takes line breaks, for prose such as a persona
export function StringField({ node }: { node: Node<'string'> }) {
  const form = useFormFields()
  const id = form.idOf(node.pointer)
  const error = form.errorOf(node.pointer)
  const value = valueAt(form.root, node.path)
  const props = {
    id,
    'value': typeof value === 'string'
      ? value
      : '',
    'placeholder': defaultHint(node),
    'aria-required': node.required || undefined,
    'aria-invalid': error ? true : undefined,
    'aria-describedby': fieldDescribedBy(node, id, error),
    'onChange': (e: { target: { value: string } }) => form.set(node.path, e.target.value === ''
      ? undefined
      : e.target.value),
  }
  return (
    <FieldShell node={node} id={id} error={error}>
      {node.schema.format === 'multiline'
        ? <Textarea rows={6} className="max-h-96" {...props} />
        : <Input {...props} />}
    </FieldShell>
  )
}

export function EnumField({ node }: { node: Node<'enum'> }) {
  const form = useFormFields()
  const id = form.idOf(node.pointer)
  const error = form.errorOf(node.pointer)
  const value = valueAt(form.root, node.path)
  const hint = defaultHint(node)
  return (
    <FieldShell node={node} id={id} error={error}>
      <NativeSelect
        id={id}
        className="w-full sm:w-64"
        value={typeof value === 'string'
          ? value
          : ''}
        aria-required={node.required || undefined}
        aria-invalid={error ? true : undefined}
        aria-describedby={fieldDescribedBy(node, id, error)}
        onChange={e => form.set(node.path, e.target.value === ''
          ? undefined
          : e.target.value)}
      >
        <NativeSelectOption value="">
          {node.required
            ? 'Choose…'
            : `Not set${hint
              ? ` (${hint.toLowerCase()})`
              : ''}`}
        </NativeSelectOption>
        {node.options.map(option => <NativeSelectOption key={option} value={option}>{option}</NativeSelectOption>)}
      </NativeSelect>
    </FieldShell>
  )
}

// typed as text and read on each change, so a half-typed number is held back with its reason, never sent as 0
export function NumberField({ node }: { node: Node<'integer' | 'number'> }) {
  const form = useFormFields()
  const id = form.idOf(node.pointer)
  const [text, setText] = useState(() => {
    const initial = valueAt(form.root, node.path)
    return typeof initial === 'number'
      ? String(initial)
      : ''
  })
  const error = form.errorOf(node.pointer)

  function change(next: string) {
    setText(next)
    const parsed = parseNumber(next, node.kind)
    form.reportLocal(node.pointer, parsed.error)
    if (!parsed.error)
      form.set(node.path, parsed.value)
  }

  return (
    <FieldShell node={node} id={id} error={error}>
      <Input
        id={id}
        className="w-full sm:w-40"
        inputMode={node.kind === 'integer'
          ? 'numeric'
          : 'decimal'}
        value={text}
        placeholder={defaultHint(node)}
        aria-required={node.required || undefined}
        aria-invalid={error ? true : undefined}
        aria-describedby={fieldDescribedBy(node, id, error)}
        onChange={e => change(e.target.value)}
      />
    </FieldShell>
  )
}
