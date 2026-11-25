import type { FieldNode } from '../json-schema'
import { useState } from 'react'
import { Textarea } from '@/components/ui/textarea'
import { useFormFields } from '../form-context'
import { valueAt } from '../json-pointer'
import { parseJson, parseJsonObject } from '../parse-json'
import { FieldMessage, FieldShell } from './field-shell'
import { fieldDescribedBy, prettyJson } from './field-text'

type RawNode = Extract<FieldNode, { kind: 'raw' }>

// what parses becomes the value at once; what does not is held back with its reason. the whole document must be an
// object (a config, a command's arguments); a part may be any json, or nothing
function useJsonText(node: RawNode, whole: boolean) {
  const form = useFormFields()
  const [text, setText] = useState(() => prettyJson(valueAt(form.root, node.path)))

  function change(next: string) {
    setText(next)
    if (next.trim() === '' && !whole) {
      form.reportLocal(node.pointer, undefined)
      form.set(node.path, undefined)
      return
    }
    const parsed = whole
      ? parseJsonObject(next)
      : parseJson(next)
    if ('error' in parsed) {
      form.reportLocal(node.pointer, parsed.error)
      return
    }
    form.reportLocal(node.pointer, undefined)
    form.set(node.path, parsed.value)
  }

  return { form, text, change }
}

const EDITOR = 'font-mono text-xs/5'

// a part of the schema the form cannot show (oneOf, patternProperties, a list of objects…): its json, every field kept
export function RawField({ node }: { node: RawNode }) {
  const { form, text, change } = useJsonText(node, false)
  const id = form.idOf(node.pointer)
  const error = form.errorOf(node.pointer)
  return (
    <FieldShell node={node} id={id} error={error}>
      <p id={`${id}-note`} className="-mt-1 text-sm text-muted-foreground">Edited as JSON: the form cannot show this part.</p>
      <Textarea
        id={id}
        className={EDITOR}
        value={text}
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        aria-required={node.required || undefined}
        aria-invalid={error ? true : undefined}
        aria-describedby={[
          `${id}-note`,
          fieldDescribedBy(node, id, error),
        ].filter(Boolean).join(' ')}
        onChange={e => change(e.target.value)}
      />
    </FieldShell>
  )
}

// the whole document as json: the fallback editor while no schema is known, and for a schema the form cannot show
export function RawDocument({ node, label, note }: { node: RawNode, label: string, note?: string }) {
  const { form, text, change } = useJsonText(node, true)
  const id = form.idOf(node.pointer)
  const error = form.errorOf(node.pointer)
  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={id} className="text-sm font-medium">{label}</label>
      {note && <p id={`${id}-note`} className="-mt-1 text-sm text-muted-foreground">{note}</p>}
      <Textarea
        id={id}
        className={EDITOR}
        value={text}
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        aria-invalid={error ? true : undefined}
        aria-describedby={[note && `${id}-note`, error && `${id}-error`].filter(Boolean).join(' ') || undefined}
        onChange={e => change(e.target.value)}
      />
      <FieldMessage id={id} error={error} />
    </div>
  )
}
