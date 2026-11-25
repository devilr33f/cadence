import type { KeyboardEvent } from 'react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { describedBy } from './fields/field-text'
import { TagChips } from './fields/tag-chips'
import { FormField } from './form-field'

// a hand-made form's list of short texts: chips with their remove buttons, and a field that adds one (Enter or Add),
// trimmed and checked first; a refused item stays in the field with its reason. the form holds the typed text, so a
// submit can refuse text that was never added (UNADDED_ITEM)
export function TagListField({ id, label, description, items, onChange, draft, onDraftChange, check, error, mono = false }: {
  id: string
  label: string
  description?: string
  items: readonly string[]
  onChange: (items: string[]) => void
  draft: string
  onDraftChange: (draft: string) => void
  // why an item may not join the list, or undefined
  check: (item: string, items: readonly string[]) => string | undefined
  error?: string
  mono?: boolean
}) {
  const [hint, setHint] = useState<string>()
  const shown = hint ?? error

  function add() {
    const item = draft.trim()
    if (item === '')
      return
    const problem = check(item, items)
    setHint(problem)
    if (problem)
      return
    onChange([...items, item])
    onDraftChange('')
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Enter') {
      event.preventDefault()
      add()
    }
  }

  return (
    <FormField id={id} label={label} description={description} error={shown}>
      <TagChips label={label} items={items} mono={mono} onRemove={i => onChange(items.filter((_, j) => j !== i))} />
      <div className="flex gap-2">
        <Input
          id={id}
          className="w-full sm:w-64"
          value={draft}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={shown ? true : undefined}
          aria-describedby={describedBy(id, { description: description !== undefined, error: shown })}
          onChange={(e) => {
            setHint(undefined)
            onDraftChange(e.target.value)
          }}
          onKeyDown={onKeyDown}
        />
        <Button type="button" variant="outline" className="h-9" aria-label={`Add to ${label}`} onClick={add}>Add</Button>
      </div>
    </FormField>
  )
}
