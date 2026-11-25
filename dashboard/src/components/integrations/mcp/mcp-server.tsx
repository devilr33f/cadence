import type { ServerView } from '@/api/gen'
import type { KeyValue } from '@/components/data/key-value-list'
import type { Tone } from '@/components/data/tone'
import { RefreshCwIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { KeyValueList } from '@/components/data/key-value-list'
import { Stamp } from '@/components/data/stamp'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { toast } from '@/components/ui/toast-manager'
import { useMcpActions } from '@/lib/mcp'
import { useTimeZone } from '@/lib/time-zone'
import { McpTools } from './mcp-tools'

const STATUS: Readonly<Record<string, { tone: Tone, label: string }>> = {
  connected: { tone: 'success', label: 'Connected' },
  connecting: { tone: 'info', label: 'Connecting' },
  unavailable: { tone: 'danger', label: 'Unavailable' },
}

const ERAS: Readonly<Record<string, string>> = {
  modern: 'Modern (streamable HTTP)',
  legacy: 'Legacy (sessions)',
}

function countsText({ total, offered, changed, skipped }: ServerView['tools']): string {
  return `${total} ${total === 1
    ? 'tool'
    : 'tools'}: ${offered} offered, ${changed} changed, ${skipped} not offered`
}

// asks the server to list its tools again (202): the outcome follows on the feed, so a toast says it was asked; a refusal
// is worded beside the button, which keeps the focus (never disabled under it)
function RefreshButton({ server }: { server: string }) {
  const actions = useMcpActions(server)
  const [pending, setPending] = useState(false)
  const [refusal, setRefusal] = useState<string>()
  async function refresh() {
    if (pending)
      return
    setPending(true)
    setRefusal(undefined)
    try {
      await actions.refresh()
      toast.add({ title: `${server} lists its tools again`, description: 'The outcome shows here once it answers.', type: 'info' })
    }
    catch (error) {
      setRefusal(`${server} was not refreshed. ${describeError(error)}`)
    }
    finally {
      setPending(false)
    }
  }
  return (
    <div className="ml-auto flex flex-wrap items-center gap-3">
      {refusal && <p role="alert" className="text-sm text-destructive">{refusal}</p>}
      <Button type="button" variant="outline" size="sm" aria-label={`Refresh ${server}`} aria-disabled={pending || undefined} onClick={() => void refresh()}>
        <RefreshCwIcon aria-hidden="true" className={pending ? 'motion-safe:animate-spin' : undefined} />
        Refresh
      </Button>
    </div>
  )
}

// one server of the mcp setting: its state, where it is (its host only) and its tools
export function McpServer({ server }: { server: ServerView }) {
  const timeZone = useTimeZone()
  const headingRef = useRef<HTMLHeadingElement>(null)
  const titleId = useId()
  const status = STATUS[server.status] ?? { tone: 'neutral', label: server.status }
  const failing = server.lastCode !== ''
  const items: KeyValue[] = [
    { label: 'Host', value: server.host || '—', mono: true },
    { label: 'Protocol', value: ERAS[server.era] ?? 'Not known yet' },
    {
      label: 'Last listed',
      value: server.lastRefresh
        ? <Stamp at={server.lastRefresh} timeZone={timeZone} />
        : <span className="text-muted-foreground">Never</span>,
    },
    ...(failing
      ? [
          { label: 'Last failure', value: server.lastCode, mono: true },
        ]
      : []),
    ...(server.failingSince
      ? [
          { label: 'Failing since', value: <Stamp at={server.failingSince} timeZone={timeZone} /> },
        ]
      : []),
    {
      label: 'Tools',
      value: countsText(server.tools),
    },
  ]
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h3 id={titleId} ref={headingRef} tabIndex={-1} className="font-mono text-base font-semibold outline-none">{server.instance}</h3>
        <StatusBadge
          tone={failing && server.status === 'connected'
            ? 'warning'
            : status.tone}
          label={status.label}
          appearance="pill"
          detail={failing
            ? <span className="font-mono text-xs">{server.lastCode}</span>
            : undefined}
        />
        <RefreshButton server={server.instance} />
      </div>
      <KeyValueList items={items} />
      <McpTools server={server.instance} heading={headingRef} />
    </section>
  )
}
