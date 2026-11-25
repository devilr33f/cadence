import type { FormEvent } from 'react'
import type { ProviderField } from './provider-refusal'
import type { CreateProviderRequest, Instance, ModuleInfo, UpdateProviderRequest } from '@/api/gen'
import type { SchemaFieldsHandle } from '@/components/forms/schema-fields'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describedBy } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogFooter } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { ProviderConfig } from './provider-config'
import { kindLabel, modulesByKind } from './provider-modules'
import { providerRefusal, secretField } from './provider-refusal'
import { ProviderSecretFields } from './provider-secret-fields'
import { clearFileText, secretInputValue } from './secret-file-data'

// the server's instance name (providers.nameRe), and the catalog's path, which no instance may take in any case
const NAME = /^[A-Z0-9][\w.-]{0,63}$/i
const NAME_RULE = 'Letters, digits, ".", "_" or "-", starting with a letter or a digit; at most 64.'
const RESERVED = 'modules'

type Errors = Partial<Record<ProviderField, string>>

interface CommonProps {
  modules: readonly ModuleInfo[]
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}

type ProviderFormProps = CommonProps & ({
  instance?: undefined
  onCreate: (request: CreateProviderRequest) => Promise<void>
  onUpdate?: undefined
} | {
  instance: Instance
  onCreate?: undefined
  onUpdate: (name: string, request: UpdateProviderRequest) => Promise<void>
})

function nameError(name: string): string | undefined {
  if (name === '')
    return 'Required.'
  if (!NAME.test(name))
    return NAME_RULE
  if (name.toLowerCase() === RESERVED)
    return 'Reserved for the module catalog.'
  return undefined
}

// name, a module from the server's catalog, its config through the module's schema and a write-only field per secret.
// an edit starts with the stored config and no secret values: an empty secret is kept, a typed one replaced
export function CreateProviderForm(props: ProviderFormProps) {
  const { modules, onDone, onPendingChange, instance } = props
  const editing = instance !== undefined
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const configRef = useRef<SchemaFieldsHandle>(null)
  const secretInputsRef = useRef(new Map<string, HTMLInputElement>())
  const [name, setName] = useState(instance?.name ?? '')
  const [moduleId, setModuleId] = useState(instance?.module ?? '')
  const [clearedSecrets, setClearedSecrets] = useState<ReadonlySet<string>>(() => new Set())
  const [errors, setErrors] = useState<Errors>({})
  const [formError, setFormError] = useState<string>()
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  const module = modules.find(m => m.id === moduleId)
  const initialConfig = (instance?.config ?? {}) as Record<string, unknown>
  const secrets = module?.secrets ?? []
  const clear = (...keys: ProviderField[]) => setErrors((current) => {
    const next = { ...current }
    for (const key of keys)
      delete next[key]
    return next
  })

  function check(): Errors {
    const found: Errors = {}
    const nameProblem = nameError(name)
    if (nameProblem)
      found.name = nameProblem
    if (!module)
      found.module = 'Choose a module.'
    for (const secret of secrets) {
      const alreadySet = instance?.secrets[secret.name]?.set === true
      const input = secretInputsRef.current.get(secret.name)
      if (secret.required && !alreadySet && (!input || secretInputValue(input) === ''))
        found[secretField(secret.name)] = 'Required.'
    }
    return found
  }

  // the values leave the inputs only for this request
  function takeSecrets(): Record<string, string> {
    const taken: Record<string, string> = {}
    for (const { name: secret } of secrets) {
      const input = secretInputsRef.current.get(secret)
      if (!input)
        continue
      const value = secretInputValue(input)
      if (!clearedSecrets.has(secret) && value !== '')
        taken[secret] = value
      clearFileText(input)
      input.value = ''
    }
    return taken
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = check()
    // the config's own fields word what they lack once a submit was tried
    const config = configRef.current?.submit()
    setErrors(found)
    setFormError(undefined)
    if (Object.keys(found).length > 0 || !module || !config) {
      setFocusTick(n => n + 1)
      return
    }
    const taken = takeSecrets()
    const clearSecrets = secrets.filter(s => clearedSecrets.has(s.name)).map(s => s.name)
    setPending(true)
    onPendingChange(true)
    try {
      if (props.instance) {
        const request: UpdateProviderRequest = {
          config,
          ...(Object.keys(taken).length > 0 && { secrets: taken }),
          ...(clearSecrets.length > 0 && { clearSecrets }),
        }
        await props.onUpdate(props.instance.name, request)
      }
      else {
        await props.onCreate({
          name,
          module: module.id,
          config,
          ...(Object.keys(taken).length > 0 && { secrets: taken }),
        })
      }
      onDone()
    }
    catch (error) {
      const refusal = providerRefusal(error, secrets.map(s => s.name))
      // a config path lands on the config's own field when one has it, else on the config as a whole
      const onConfigField = refusal.config !== undefined
        && configRef.current?.refuse(refusal.config.pointer, refusal.config.message) === true
      if (!onConfigField && refusal.field)
        setErrors({ [refusal.field]: refusal.message })
      else if (!onConfigField)
        setFormError(refusal.message)
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const id = (field: string) => `${baseId}-${field}`
  const nameDescription = editing ? 'The instance keeps its name.' : 'Settings name the instance by it.'
  return (
    <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-5">
      <FormField id={id('name')} label="Name" required description={nameDescription} error={errors.name}>
        <Input
          id={id('name')}
          value={name}
          readOnly={editing}
          autoComplete="off"
          spellCheck={false}
          aria-required
          aria-invalid={errors.name ? true : undefined}
          aria-describedby={describedBy(id('name'), { description: true, error: errors.name })}
          onChange={(e) => {
            setName(e.target.value)
            clear('name')
          }}
        />
      </FormField>
      <FormField id={id('module')} label="Module" required error={errors.module}>
        <NativeSelect
          id={id('module')}
          className="w-full"
          value={moduleId}
          disabled={editing}
          aria-required
          aria-invalid={errors.module ? true : undefined}
          aria-describedby={describedBy(id('module'), { error: errors.module })}
          onChange={(e) => {
            setModuleId(e.target.value)
            clear('module', 'config', 'secrets', ...secrets.map(s => secretField(s.name)))
          }}
        >
          <NativeSelectOption value="">Choose a module</NativeSelectOption>
          {modulesByKind(modules).map(group => (
            <optgroup key={group.kind} label={kindLabel(group.kind)}>
              {group.modules.map(m => <NativeSelectOption key={m.id} value={m.id}>{m.id}</NativeSelectOption>)}
            </optgroup>
          ))}
        </NativeSelect>
      </FormField>
      {/* keyed by the module: a value typed for one service never goes to another */}
      {module && <ProviderConfig key={`config-${module.id}`} ref={configRef} id={id('config')} module={module} initial={initialConfig} error={errors.config} />}
      <ProviderSecretFields
        key={`secrets-${moduleId}`}
        baseId={baseId}
        fields={secrets}
        states={instance?.secrets}
        cleared={clearedSecrets}
        inputs={secretInputsRef}
        errors={errors}
        onChange={field => clear(field, 'secrets')}
        onClear={(secret, clearIt) => {
          setClearedSecrets((current) => {
            const next = new Set(current)
            if (clearIt)
              next.add(secret)
            else
              next.delete(secret)
            return next
          })
          clear(secretField(secret), 'secrets')
        }}
      />
      {formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          {editing ? 'Save changes' : 'Add the provider'}
        </Button>
      </DialogFooter>
    </form>
  )
}
