import type { StatusJson } from '@/api/gen'
import type { KeyValue } from '@/components/data/key-value-list'
import type { Tone } from '@/components/data/tone'
import { KeyValueList } from '@/components/data/key-value-list'
import { Stamp } from '@/components/data/stamp'
import { StatusBadge } from '@/components/data/status-badge'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { useTimeZone } from '@/lib/time-zone'

// what the client is doing: a setting changed since the start (or a refused token) waits for a restart, and a failure
// stays on the board with its code
function stateOf(status: StatusJson): { tone: Tone, label: string, code?: string } {
  if (status.restartRequired)
    return { tone: 'warning', label: 'Restart required', code: status.lastError ?? undefined }
  if (status.connected)
    return { tone: 'success', label: 'Connected' }
  if (status.lastError)
    return { tone: 'danger', label: 'Failed', code: status.lastError }
  return { tone: 'info', label: 'Connecting' }
}

function count(n: number, one: string, many: string): string {
  return `${n} ${n === 1
    ? one
    : many}`
}

function Mono({ value }: { value: string | null }) {
  return value === null
    ? <span className="text-muted-foreground">None</span>
    : <code className="font-mono text-xs">{value}</code>
}

export function HomeAssistantStatus({ status }: { status: StatusJson }) {
  const timeZone = useTimeZone()
  const state = stateOf(status)
  const items: KeyValue[] = [
    { label: 'Running instance', value: <Mono value={status.instance} /> },
    { label: 'Setting', value: <Mono value={status.configured} /> },
    // what its latest authentication reported; none before one did
    ...(status.version === null
      ? []
      : [
          { label: 'Version', value: <Mono value={status.version} /> },
        ]),
    {
      label: 'Connected since',
      value: status.connected && status.connectedAt
        ? <Stamp at={status.connectedAt} timeZone={timeZone} />
        : <span className="text-muted-foreground">Not connected</span>,
    },
    { label: 'Home', value: `${count(status.entities, 'entity', 'entities')} in ${count(status.areas, 'area', 'areas')}` },
    ...(status.lastError
      ? [
          { label: 'Last error', value: status.lastError, mono: true },
        ]
      : []),
  ]
  return (
    <Card size="sm">
      <CardHeader className="flex flex-wrap items-center gap-3">
        <h3 className="text-base font-semibold">Connection</h3>
        <StatusBadge tone={state.tone} label={state.label} appearance="pill" detail={state.code && <span className="font-mono text-xs">{state.code}</span>} />
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <KeyValueList items={items} />
        {status.restartRequired && (
          <p className="text-sm text-muted-foreground">
            The client runs with what the server read at its start: restart the server to apply the setting, or a fixed token.
          </p>
        )}
      </CardContent>
    </Card>
  )
}
