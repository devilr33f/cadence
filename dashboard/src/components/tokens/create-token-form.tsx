import type { FormEvent } from 'react'
import type { CreatedToken, CreateTokenRequest } from '@/api/gen'
import type { TokenScope } from '@/lib/tokens'
import { useQuery } from '@tanstack/react-query'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { useApi } from '@/api/use-api'
import { describedBy } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { segmentsOf } from '@/components/forms/json-pointer'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { mcpToolsQueryOptions, servedTools } from '@/lib/tokens'
import { TokenScopesField } from './token-scopes-field'
import { TokenToolsField } from './token-tools-field'

// the server's token name (CreateTokenRequest's pattern)
const NAME = /^[a-z0-9][a-z0-9._-]{0,63}$/
const NAME_RULE = 'Lowercase letters, digits, ".", "_" or "-", starting with a letter or a digit; at most 64.'

interface Errors {
  name?: string
  scopes?: string
  form?: string
}

// a taken name is the name's; a 422's path (huma's /body/<field>) names the name or the scopes; anything else is the
// form's, in codes
function refusalOf(error: unknown): Errors {
  if (!(error instanceof ApiError)) {
    return {
      form: describeError(error),
    }
  }
  if (error.error === 'name_taken') {
    return {
      name: describeError(error),
    }
  }
  const segments = segmentsOf(error.path ?? '')
  const field = segments[0] === 'body'
    ? segments[1]
    : segments[0]
  if (error.status === 422 && (field === 'name' || field === 'scopes'))
    return { [field]: `The server refused this value (${error.error}).` }
  return {
    form: describeError(error),
  }
}

function check(name: string, scopes: readonly TokenScope[]): Errors {
  const found: Errors = {}
  if (name === '')
    found.name = 'Required.'
  else if (!NAME.test(name))
    found.name = NAME_RULE
  if (scopes.length === 0)
    found.scopes = 'Choose at least one scope.'
  return found
}

export function CreateTokenForm({ onCreate, onCreated, onPendingChange }: {
  onCreate: (request: CreateTokenRequest) => Promise<CreatedToken>
  onCreated: (created: CreatedToken) => void
  onPendingChange: (pending: boolean) => void
}) {
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const api = useApi()
  const mcpTools = useQuery(mcpToolsQueryOptions(api))
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<TokenScope[]>(['admin'])
  const [tools, setTools] = useState<string[]>([])
  const [errors, setErrors] = useState<Errors>({})
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = check(name, scopes)
    setErrors(found)
    if (Object.keys(found).length > 0) {
      setFocusTick(n => n + 1)
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      const wantsMcp = scopes.includes('mcp')
      onCreated(await onCreate({
        name,
        scopes,
        mcpTools: wantsMcp
          ? servedTools(mcpTools.data, tools)
          : undefined,
      }))
    }
    catch (error) {
      setErrors(refusalOf(error))
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const nameId = `${baseId}-name`
  const nameDescription = 'Say what uses it, such as home-assistant.'
  return (
    <>
      <DialogHeader>
        <DialogTitle>Create a token</DialogTitle>
        <DialogDescription>A bearer token for a script, Home Assistant or a metrics scraper, with the scopes it may use.</DialogDescription>
      </DialogHeader>
      <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-5">
        <FormField id={nameId} label="Name" required description={nameDescription} error={errors.name}>
          <Input
            id={nameId}
            value={name}
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="off"
            aria-required
            aria-invalid={errors.name ? true : undefined}
            aria-describedby={describedBy(nameId, { description: true, error: errors.name })}
            onChange={(e) => {
              setName(e.target.value)
              setErrors(({ name: _, ...rest }) => rest)
            }}
          />
        </FormField>
        <TokenScopesField
          id={`${baseId}-scopes`}
          value={scopes}
          error={errors.scopes}
          onChange={(next) => {
            setScopes(next)
            setErrors(({ scopes: _, ...rest }) => rest)
          }}
        />
        {scopes.includes('mcp') && (
          <TokenToolsField
            id={`${baseId}-tools`}
            tools={mcpTools.data}
            value={tools}
            onChange={setTools}
          />
        )}
        {errors.form && <p role="alert" className="text-sm text-destructive">{errors.form}</p>}
        <DialogFooter>
          <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
          <Button type="submit" aria-disabled={pending || undefined}>
            {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
            Create the token
          </Button>
        </DialogFooter>
      </form>
    </>
  )
}
