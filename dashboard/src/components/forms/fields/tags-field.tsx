import type { KeyboardEvent } from 'react'
import type { FieldNode } from '../json-schema'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useFormFields } from '../form-context'
import { valueAt } from '../json-pointer'
import { parseNumber } from '../parse-number'
import { FieldMessage, FieldShell, RequiredMark } from './field-shell'
import { fieldDescribedBy, fieldLabel, UNADDED_ITEM } from './field-text'
import { TagChips } from './tag-chips'

type TagsNode = Extract<FieldNode, { kind: 'tags' }>

// an emptied optional list is left out; a required one stays, so its minimum can be named
function useList(node: TagsNode) {
  const form = useFormFields()
  const value = valueAt(form.root, node.path)
  const items = Array.isArray(value)
    ? value as (string | number)[]
    : []
  const set = (next: (string | number)[]) => form.set(node.path, next.length === 0 && !node.required
    ? undefined
    : next)
  return { form, items, set }
}

// a fixed set of options: one checkbox each, kept in the schema's order; each is marked when the list is wrong, so the
// submit's focus lands on the first
function OptionsField({ node, options }: { node: TagsNode, options: readonly string[] }) {
  const { form, items, set } = useList(node)
  const id = form.idOf(node.pointer)
  const error = form.errorOf(node.pointer)
  const description = typeof node.schema.description === 'string'
    ? node.schema.description
    : undefined
  return (
    <fieldset className="flex flex-col gap-2" aria-describedby={fieldDescribedBy(node, id, error)}>
      <legend className={typeof node.schema.title === 'string'
        ? 'mb-2 text-sm font-medium'
        : 'mb-2 font-mono text-sm font-medium'}
      >
        {fieldLabel(node)}
        <RequiredMark required={node.required} />
      </legend>
      {description && <p id={`${id}-description`} className="-mt-1 text-sm text-muted-foreground">{description}</p>}
      <div className="flex flex-wrap gap-x-4 gap-y-2">
        {options.map(option => (
          <label key={option} className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              className="size-4"
              aria-invalid={error ? true : undefined}
              checked={items.includes(option)}
              onChange={e => set(e.target.checked
                ? options.filter(o => o === option || items.includes(o))
                : items.filter(i => i !== option))}
            />
            {option}
          </label>
        ))}
      </div>
      <FieldMessage id={id} error={error} />
    </fieldset>
  )
}

// free items: a chip each with its remove button, and a field that adds one (Enter or Add)
function FreeField({ node }: { node: TagsNode }) {
  const { form, items, set } = useList(node)
  const [draft, setDraft] = useState('')
  const [hint, setHint] = useState<string>()

  // text typed but not added would be lost on submit: it holds the submit back until it is added or cleared
  function type(next: string) {
    setDraft(next)
    form.reportLocal(node.pointer, next.trim() === ''
      ? undefined
      : UNADDED_ITEM)
  }
  const id = form.idOf(node.pointer)
  const error = hint ?? form.errorOf(node.pointer)
  const label = fieldLabel(node)

  function add() {
    if (draft.trim() === '')
      return
    let item: string | number = draft
    if (node.item.kind === 'integer' || node.item.kind === 'number') {
      const parsed = parseNumber(draft, node.item.kind)
      if (parsed.error || parsed.value === undefined) {
        setHint(parsed.error)
        return
      }
      item = parsed.value
    }
    setHint(undefined)
    set([...items, item])
    type('')
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Enter') {
      event.preventDefault()
      add()
    }
  }

  return (
    <FieldShell node={node} id={id} error={error}>
      <TagChips label={label} items={items} onRemove={i => set(items.filter((_, j) => j !== i))} />
      <div className="flex gap-2">
        <Input
          id={id}
          className="w-full sm:w-64"
          value={draft}
          aria-required={node.required || undefined}
          aria-invalid={error ? true : undefined}
          aria-describedby={fieldDescribedBy(node, id, error)}
          onChange={e => type(e.target.value)}
          onKeyDown={onKeyDown}
        />
        <Button type="button" variant="outline" className="h-9" aria-label={`Add to ${label}`} onClick={add}>Add</Button>
      </div>
    </FieldShell>
  )
}

export function TagsField({ node }: { node: TagsNode }) {
  return node.item.kind === 'enum'
    ? <OptionsField node={node} options={node.item.options} />
    : <FreeField node={node} />
}
