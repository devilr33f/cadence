import type { SettingSpec, SettingsScopeKind } from './settings-catalog'
import type { SettingsScope } from '@/lib/settings'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useLocation } from '@tanstack/react-router'
import { cn } from 'cn'
import { LoaderCircleIcon, RotateCcwIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { JsonForm } from '@/components/forms/json-form'
import { Button } from '@/components/ui/button'
import { buttonVariants } from '@/components/ui/button-variants'
import { TableCell, TableRow } from '@/components/ui/table'
import { useFocusReturn } from '@/hooks/use-focus-return'
import { putSetting } from '@/lib/settings'
import { settingText } from './settings-catalog'

const COLUMNS = 4

// a device overrides what the global scope sets; the global scope sets what the server would default
const WORDS: Record<SettingsScopeKind, { start: string, save: string, own: string }> = {
  device: { start: 'Override', save: 'Save the override', own: 'Overridden' },
  global: { start: 'Set', save: 'Save the setting', own: 'Set' },
}

// the editor opens on what is in force, so an override starts from the inherited value; a switch has to start somewhere
function startingValue(spec: SettingSpec, own: unknown, inherited: unknown): unknown {
  return own ?? inherited ?? (spec.schema.type === 'boolean'
    ? false
    : undefined)
}

function source(scope: SettingsScopeKind, own: unknown, inherited: unknown): string {
  if (own !== undefined)
    return WORDS[scope].own
  return inherited !== undefined
    ? 'Inherited'
    : 'Server default'
}

// one key: the value in force and where it comes from; set or edit it through its schema's form, or reset it (null
// removes the scope's own value: a device's falls back to the global one, the global one to the server's default)
export function SettingRow({ scope, spec, own, inherited }: {
  scope: SettingsScope
  spec: SettingSpec
  own: unknown
  inherited: unknown
}) {
  const api = useApi()
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [resetError, setResetError] = useState<string>()
  const words = WORDS[scope.kind]
  const isOwn = own !== undefined
  // the start/Edit button is the row's anchor: the editor hands the focus back to it, and so does a reset, whose own
  // button goes with the value
  const back = useFocusReturn<HTMLButtonElement>(editing)
  const close = () => {
    back.arm()
    setEditing(false)
  }
  // the integrations page links this row (its hash, without the #): the row is scrolled to and marked, and its button
  // takes the focus, so the keyboard carries on from here
  const linked = useLocation({
    select: location => location.hash === `setting-${spec.key}`,
  })
  useEffect(() => {
    if (linked)
      back.ref.current?.focus()
  }, [linked, back.ref])

  async function reset() {
    if (resetting)
      return
    setResetting(true)
    setResetError(undefined)
    try {
      await putSetting(api, queryClient, scope, spec.key, null)
      back.ref.current?.focus()
    }
    catch (error) {
      setResetError(describeError(error))
    }
    finally {
      setResetting(false)
    }
  }

  const action = isOwn ? 'Edit' : words.start
  const start = startingValue(spec, own, inherited)
  return (
    <>
      {/* the integrations page links a key's row (/settings#setting-<key>) */}
      <TableRow id={`setting-${spec.key}`} aria-label={spec.label} className={cn('scroll-mt-20 target:bg-accent', isOwn && 'bg-accent/40 hover:bg-accent/60')}>
        <TableCell className="py-1.5 whitespace-normal">
          <span className="block font-medium">{spec.label}</span>
          <span className="block font-mono text-xs text-muted-foreground">{spec.key}</span>
          {spec.manage && (
            <Link to={spec.manage.to} className={buttonVariants({ variant: 'link', className: 'text-xs' })}>
              {spec.manage.label}
            </Link>
          )}
        </TableCell>
        <TableCell className="whitespace-normal">{settingText(spec, own ?? inherited)}</TableCell>
        <TableCell className={isOwn ? 'font-medium' : 'text-muted-foreground'}>{source(scope.kind, own, inherited)}</TableCell>
        <TableCell className="py-0 pr-1.5 text-right">
          <div className="flex justify-end gap-1">
            {!editing && (
              <Button ref={back.ref} type="button" variant="ghost" size="sm" aria-label={`${action} ${spec.label}`} onClick={() => setEditing(true)}>
                {action}
              </Button>
            )}
            {isOwn && (
              <Button type="button" variant="ghost" size="sm" aria-label={`Reset ${spec.label}`} aria-disabled={resetting || undefined} onClick={() => void reset()}>
                {resetting
                  ? <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />
                  : <RotateCcwIcon aria-hidden="true" />}
                Reset
              </Button>
            )}
          </div>
          {resetError && <p role="alert" className="pb-2 text-left text-sm whitespace-normal text-destructive">{resetError}</p>}
        </TableCell>
      </TableRow>
      {editing && (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={COLUMNS} className="h-auto bg-muted/30 p-4 whitespace-normal">
            <JsonForm
              autoFocus
              schema={{
                type: 'object',
                properties: {
                  [spec.key]: { ...spec.schema, title: spec.label },
                },
                required: [spec.key],
                additionalProperties: false,
              }}
              initial={start === undefined
                ? {}
                : { [spec.key]: start }}
              label={`${action} ${spec.label}`}
              submitLabel={words.save}
              onCancel={close}
              onSubmit={async (value) => {
                await putSetting(api, queryClient, scope, spec.key, value[spec.key])
                close()
              }}
            />
          </TableCell>
        </TableRow>
      )}
    </>
  )
}
