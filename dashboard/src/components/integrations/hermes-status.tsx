import type { ReactNode } from 'react'
import type { KindJson } from '@/api/gen'
import type { Tone } from '@/components/data/tone'
import { useQuery } from '@tanstack/react-query'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { LoadingLines } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { taskKindsQueryOptions } from '@/lib/tasks'
import { SettingLink } from './setting-link'

function Code({ children }: { children: ReactNode }) {
  return <code className="font-mono text-xs">{children}</code>
}

// the hermes setting is read at startup: what the running kind was built with against what the setting says now
function stateOf(kind: KindJson | undefined): { tone: Tone, label: string, text: ReactNode } {
  const instance = kind?.instance ?? null
  const configured = kind?.configured ?? null
  if (instance === null && configured === null)
    return { tone: 'neutral', label: 'Not set up', text: 'Add a Hermes provider and name it in the hermes setting, then restart the server.' }
  if (instance === configured) {
    return {
      tone: 'success',
      label: 'Running',
      text: (
        <>
          Tasks run on
          {' '}
          <Code>{instance}</Code>
          .
        </>
      ),
    }
  }
  return {
    tone: 'warning',
    label: 'Restart required',
    text: (
      <>
        The setting names
        {' '}
        {configured === null
          ? 'no instance'
          : <Code>{configured}</Code>}
        ; the server runs
        {' '}
        {instance === null
          ? 'none'
          : <Code>{instance}</Code>}
        . Restart it to apply the setting.
      </>
    ),
  }
}

// a status line: hermes has no page of its own, its tasks are on the tasks page and its switch is a setting
export function HermesStatus() {
  const api = useApi()
  const kinds = useQuery(taskKindsQueryOptions(api))
  if (kinds.isPending)
    return <LoadingLines lines={1} label="Loading the task kinds" />
  if (kinds.isError)
    return <p className="text-sm text-muted-foreground">{`The task kinds could not be read. ${describeError(kinds.error)}`}</p>
  const state = stateOf(kinds.data.find(kind => kind.name === 'hermes'))
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-base">
      <StatusBadge tone={state.tone} label={state.label} appearance="pill" />
      <span>{state.text}</span>
      <SettingLink settingKey="hermes" />
    </div>
  )
}
