import type { FormEvent } from 'react'
import type { SkillDraft, SkillErrors, SkillField } from './skill-draft'
import type { PutSkillRequest, SkillView } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { describedBy, UNADDED_ITEM } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { refusedText } from '@/components/forms/refusal'
import { TagListField } from '@/components/forms/tag-list-field'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogFooter } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { checkDraft, draftOf, fieldOf, modeError, NEW_SKILL, requestOf, toolError, triggerError } from './skill-draft'

type ListField = 'triggers' | 'tools' | 'modes'

const NO_DRAFTS: Record<ListField, string> = { triggers: '', tools: '', modes: '' }

// a switch with its label and what it does, as the modes form sets its own
function SwitchRow({ id, label, description, checked, onChange }: {
  id: string
  label: string
  description: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-start gap-3">
      <Switch
        className="mt-0.5"
        checked={checked}
        aria-labelledby={`${id}-label`}
        aria-describedby={describedBy(id, { description: true })}
        onCheckedChange={onChange}
      />
      <div className="flex flex-col gap-0.5">
        <span id={`${id}-label`} className="text-sm font-medium">{label}</span>
        <span id={`${id}-description`} className="text-sm text-muted-foreground">{description}</span>
      </div>
    </div>
  )
}

// every field of a skill; a create names it (a put under a name another skill has would replace it, so a create
// refuses the names the list holds), an edit keeps its name; a built-in's edit stores the owner's copy, which replaces it
export function SkillForm({ skill, taken, onSave, onDone, onPendingChange }: {
  skill?: SkillView
  taken: readonly string[]
  onSave: (name: string, body: PutSkillRequest) => Promise<void>
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const creating = skill === undefined
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const [draft, setDraft] = useState<SkillDraft>(() => skill
    ? draftOf(skill)
    : NEW_SKILL)
  const [typed, setTyped] = useState(NO_DRAFTS)
  const [errors, setErrors] = useState<SkillErrors>({})
  const [formError, setFormError] = useState<string>()
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  const change = <K extends keyof SkillDraft>(key: K, value: SkillDraft[K], field?: SkillField) => {
    setDraft(current => ({ ...current, [key]: value }))
    const f = field ?? (key as SkillField)
    setErrors(({ [f]: _, ...rest }) => rest)
  }
  const type = (field: ListField, text: string) => {
    setTyped(current => ({ ...current, [field]: text }))
    setErrors(({ [field]: _, ...rest }) => rest)
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = checkDraft(draft, creating, taken)
    const lists: ListField[] = draft.allModes
      ? ['triggers', 'tools']
      : ['triggers', 'tools', 'modes']
    for (const field of lists) {
      if (typed[field].trim() !== '')
        found[field] = UNADDED_ITEM
    }
    setErrors(found)
    setFormError(undefined)
    if (Object.keys(found).length > 0) {
      setFocusTick(n => n + 1)
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      await onSave(draft.name, requestOf(draft))
      onDone()
    }
    catch (error) {
      const field = error instanceof ApiError
        ? fieldOf(error.path)
        : undefined
      if (field && error instanceof ApiError && (field !== 'name' || creating)) {
        setErrors({
          [field]: refusedText(error),
        })
      }
      else {
        setFormError(describeError(error))
      }
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const id = (field: string) => `${baseId}-${field}`
  const control = (field: SkillField, description = false) => ({
    'id': id(field),
    'aria-invalid': errors[field] ? true : undefined,
    'aria-describedby': describedBy(id(field), { description, error: errors[field] }),
  })
  return (
    <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-5">
      {creating && (
        <FormField id={id('name')} label="Name" required description="How turns and the load_skill tool name it." error={errors.name}>
          <Input {...control('name', true)} className="font-mono text-sm" value={draft.name} spellCheck={false} autoCapitalize="off" aria-required onChange={e => change('name', e.target.value)} />
        </FormField>
      )}
      <SwitchRow
        id={id('enabled')}
        label="On"
        description="Off, no turn lists or loads it; a built-in switched off stays off until this copy is deleted."
        checked={draft.enabled}
        onChange={on => change('enabled', on)}
      />
      <FormField id={id('description')} label="Description" required description="One line every turn's skill list shows the model, up to 200 characters." error={errors.description}>
        <Input {...control('description', true)} value={draft.description} aria-required onChange={e => change('description', e.target.value)} />
      </FormField>
      <FormField id={id('body')} label="Instructions" required description="What the model reads once the skill is loaded, up to 4000 characters. Short rules work best." error={errors.body}>
        <Textarea {...control('body', true)} className="min-h-40 font-mono text-xs" value={draft.body} aria-required onChange={e => change('body', e.target.value)} />
      </FormField>
      <TagListField
        id={id('triggers')}
        label="Triggers"
        description="Words and phrases that load it before the first step when said, in any case and word form (свет matches света)."
        items={draft.triggers}
        onChange={items => change('triggers', items)}
        draft={typed.triggers}
        onDraftChange={text => type('triggers', text)}
        check={triggerError}
        error={errors.triggers}
      />
      <TagListField
        id={id('tools')}
        label="Tools"
        description="The tools it is about: a turn offered none of them does not list it. Empty lists it always."
        mono
        items={draft.tools}
        onChange={items => change('tools', items)}
        draft={typed.tools}
        onDraftChange={text => type('tools', text)}
        check={toolError}
        error={errors.tools}
      />
      <fieldset className="flex flex-col gap-4">
        <legend className="mb-1 text-sm font-medium">Modes</legend>
        <SwitchRow
          id={id('all-modes')}
          label="Every mode"
          description="Off, only the modes named here list it, none while the list is empty."
          checked={draft.allModes}
          onChange={all => change('allModes', all, 'modes')}
        />
        {!draft.allModes && (
          <TagListField
            id={id('modes')}
            label="Modes"
            mono
            items={draft.modes}
            onChange={items => change('modes', items)}
            draft={typed.modes}
            onDraftChange={text => type('modes', text)}
            check={modeError}
            error={errors.modes}
          />
        )}
      </fieldset>
      {formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          {creating ? 'Add the skill' : 'Save the skill'}
        </Button>
      </DialogFooter>
    </form>
  )
}
