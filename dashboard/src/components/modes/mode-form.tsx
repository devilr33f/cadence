import type { FormEvent } from 'react'
import type { ModeDraft, ModeErrors, ModeField } from './mode-draft'
import type { ModeView, PutModeRequest } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describedBy, UNADDED_ITEM } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogFooter } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { checkDraft, draftOf, INTERIM, NEW_MODE, REASONING, requestOf } from './mode-draft'
import { ModeAliasesFields, ModeToolsField } from './mode-lists'
import { modeRefusal } from './mode-refusal'
import { INTERIM_LABELS, REASONING_LABELS } from './mode-words'

type ListField = 'tools' | 'en' | 'ru'

const NO_DRAFTS: Record<ListField, string> = { tools: '', en: '', ru: '' }

// every field of a mode; a create names it (a put under a name another mode has would replace that one, so a create
// refuses the names the list holds), an edit keeps its name
export function ModeForm({ mode, taken, onSave, onDone, onPendingChange }: {
  mode?: ModeView
  taken: readonly string[]
  onSave: (name: string, body: PutModeRequest) => Promise<void>
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const creating = mode === undefined
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const [draft, setDraft] = useState<ModeDraft>(() => mode
    ? draftOf(mode)
    : NEW_MODE)
  const [typed, setTyped] = useState(NO_DRAFTS)
  const [errors, setErrors] = useState<ModeErrors>({})
  const [formError, setFormError] = useState<string>()
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  const change = <K extends keyof ModeDraft>(key: K, value: ModeDraft[K], field: ModeField = key as ModeField) => {
    setDraft(current => ({ ...current, [key]: value }))
    setErrors(({ [field]: _, ...rest }) => rest)
  }
  const type = (field: ListField, text: string) => {
    setTyped(current => ({ ...current, [field]: text }))
    setErrors(({ [field]: _, ...rest }) => rest)
  }

  function check(): ModeErrors {
    const found = checkDraft(draft, creating, taken)
    const lists: ListField[] = draft.allTools
      ? ['en', 'ru']
      : ['tools', 'en', 'ru']
    for (const field of lists) {
      if (typed[field].trim() !== '')
        found[field] = UNADDED_ITEM
    }
    return found
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = check()
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
      const refusal = modeRefusal(error, draft, creating)
      if (refusal.field)
        setErrors({ [refusal.field]: refusal.message })
      else
        setFormError(refusal.message)
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const id = (field: string) => `${baseId}-${field}`
  const control = (field: ModeField, description = false) => ({
    'id': id(field),
    'aria-invalid': errors[field] ? true : undefined,
    'aria-describedby': describedBy(id(field), { description, error: errors[field] }),
  })
  return (
    <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-5">
      {creating && (
        <FormField id={id('name')} label="Name" required description="Settings and the agent_mode tool name it by this." error={errors.name}>
          <Input {...control('name', true)} value={draft.name} spellCheck={false} autoCapitalize="off" aria-required onChange={e => change('name', e.target.value)} />
        </FormField>
      )}
      <FormField id={id('model')} label="Model" description="A model id at the device's language model instance; empty uses the device's model setting." error={errors.model}>
        <Input {...control('model', true)} className="font-mono text-sm" value={draft.model} spellCheck={false} autoCapitalize="off" onChange={e => change('model', e.target.value)} />
      </FormField>
      <FormField id={id('reasoning')} label="Reasoning" required error={errors.reasoning}>
        <NativeSelect {...control('reasoning')} className="w-full sm:w-64" value={draft.reasoning} aria-required onChange={e => change('reasoning', e.target.value as ModeDraft['reasoning'])}>
          {REASONING.map(r => <NativeSelectOption key={r} value={r}>{REASONING_LABELS[r]}</NativeSelectOption>)}
        </NativeSelect>
      </FormField>
      <FormField id={id('maxSteps')} label="Steps" required description="Model calls a turn makes before its answer is forced, 1 to 50." error={errors.maxSteps}>
        <Input {...control('maxSteps', true)} className="w-full sm:w-40" inputMode="numeric" value={draft.maxSteps} aria-required onChange={e => change('maxSteps', e.target.value)} />
      </FormField>
      <FormField id={id('maxSeconds')} label="Seconds" required description="Seconds a turn takes before its answer is forced, 5 to 600." error={errors.maxSeconds}>
        <Input {...control('maxSeconds', true)} className="w-full sm:w-40" inputMode="numeric" value={draft.maxSeconds} aria-required onChange={e => change('maxSeconds', e.target.value)} />
      </FormField>
      <FormField id={id('firstTokenSeconds')} label="First token timeout" description="Seconds the model may take before its first token, 2 to 120; slow reasoning models need more. Empty keeps the built-in 10." error={errors.firstTokenSeconds}>
        <Input {...control('firstTokenSeconds', true)} className="w-full sm:w-40" inputMode="numeric" value={draft.firstTokenSeconds} onChange={e => change('firstTokenSeconds', e.target.value)} />
      </FormField>
      <FormField id={id('maxTokens')} label="Max tokens" description="The ceiling on the model's tokens a step may write, 64 to 32768; empty keeps the built-in default (4096 with reasoning, else 1024)." error={errors.maxTokens}>
        <Input {...control('maxTokens', true)} className="w-full sm:w-40" inputMode="numeric" value={draft.maxTokens} onChange={e => change('maxTokens', e.target.value)} />
      </FormField>
      <FormField id={id('interim')} label="Interim text" required description="Whether text that comes with tool calls is spoken." error={errors.interim}>
        <NativeSelect {...control('interim', true)} className="w-full sm:w-64" value={draft.interim} aria-required onChange={e => change('interim', e.target.value as ModeDraft['interim'])}>
          {INTERIM.map(i => <NativeSelectOption key={i} value={i}>{INTERIM_LABELS[i]}</NativeSelectOption>)}
        </NativeSelect>
      </FormField>
      <ModeToolsField
        baseId={baseId}
        allTools={draft.allTools}
        tools={draft.tools}
        typed={typed.tools}
        error={errors.tools}
        onAllTools={all => change('allTools', all, 'tools')}
        onTools={tools => change('tools', tools)}
        onType={text => type('tools', text)}
      />
      <ModeAliasesFields
        baseId={baseId}
        en={draft.en}
        ru={draft.ru}
        typed={typed}
        errors={errors}
        onChange={(lang, words) => change(lang, words)}
        onType={type}
      />
      {formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          {creating ? 'Add the mode' : 'Save the mode'}
        </Button>
      </DialogFooter>
    </form>
  )
}
