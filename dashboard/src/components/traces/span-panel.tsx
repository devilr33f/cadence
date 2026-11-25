import type { ReactNode } from 'react'
import type { SpanView } from '@/api/gen'
import { Link } from '@tanstack/react-router'
import { useId } from 'react'
import { KeyValueList } from '@/components/data/key-value-list'
import { useNow } from '@/hooks/use-now'
import { failureFacts } from '@/lib/traces/failure'
import { attrText, rateText, serviceLabel, spanDurationText } from '@/lib/traces/format'
import { liveDuration } from '@/lib/traces/waterfall'
import { bytesText } from '@/lib/units'
import { TraceStatus } from './trace-status'

function Block({ title, children }: { title: string, children: ReactNode }) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="flex flex-col gap-2 border-t px-4 pt-3">
      <h3 id={id} className="text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  )
}

function Attributes({ attrs }: { attrs: Record<string, unknown> }) {
  const keys = Object.keys(attrs).sort()
  if (keys.length === 0)
    return <p className="text-sm text-muted-foreground">None</p>
  return (
    <dl className="grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-xs/5">
      {keys.map(key => (
        <div key={key} className="contents">
          <dt className="break-all text-muted-foreground">{key}</dt>
          <dd className="break-all">{attrText(attrs[key])}</dd>
        </div>
      ))}
    </dl>
  )
}

// the selected span: what it is, where it ran and how it ended, its attributes, its events from its start, its
// bytes and its links to other traces
export function SpanPanel({ span, deviceName }: { span: SpanView, deviceName?: string }) {
  const now = useNow(span.live)
  const duration = now === null
    ? span.durationMs
    : liveDuration(span, now)
  const start = Date.parse(span.start)
  const items = [
    { label: 'Status', value: <TraceStatus live={span.live} error={span.status === 'error'} /> },
    { label: 'Duration', value: <span data-numeric>{spanDurationText(duration)}</span> },
    {
      label: 'Ran on',
      value: serviceLabel(span.service),
    },
    ...(span.deviceId
      ? [
          { label: 'Speaker', value: deviceName || span.deviceName || span.deviceId },
        ]
      : []),
    ...(span.board
      ? [
          { label: 'Board', value: span.board, mono: true },
        ]
      : []),
    ...(span.scope
      ? [
          { label: 'Scope', value: span.scope, mono: true },
        ]
      : []),
    ...(span.kind
      ? [
          { label: 'Kind', value: span.kind },
        ]
      : []),
    { label: 'Span id', value: span.spanId, mono: true },
    ...(span.parentId
      ? [
          { label: 'Parent', value: `${span.parentId}${span.remoteParent ? ' (remote)' : ''}`, mono: true },
        ]
      : []),
  ]
  const events = span.events ?? []
  const links = span.links ?? []
  const failure = failureFacts(span)
  return (
    <aside aria-label="The selected span" className="flex flex-col gap-3 rounded-lg border bg-card pb-4">
      <h2 className="px-4 pt-4 font-mono text-sm font-semibold break-all">{span.name}</h2>
      {failure.length > 0 && (
        <section aria-label="Why it failed" className="mx-4 rounded-md border border-destructive/40 bg-destructive-surface px-3 py-2">
          <KeyValueList
            className="gap-y-1.5"
            items={failure.map(f => ({ label: f.label, value: <span className="text-destructive">{f.value}</span>, mono: true }))}
          />
        </section>
      )}
      <div className="px-4">
        <KeyValueList items={items} />
      </div>
      {span.stream && (
        <Block title="Stream">
          <KeyValueList items={[
            { label: 'In', value: <span data-numeric>{`${bytesText(span.stream.bytesIn)} · ${rateText(span.live ? span.stream.speedIn : 0)}`}</span> },
            { label: 'Out', value: <span data-numeric>{`${bytesText(span.stream.bytesOut)} · ${rateText(span.live ? span.stream.speedOut : 0)}`}</span> },
          ]}
          />
        </Block>
      )}
      <Block title="Attributes">
        <Attributes attrs={span.attrs ?? {}} />
      </Block>
      {events.length > 0 && (
        <Block title="Events">
          <ol className="flex flex-col gap-1.5">
            {events.map(event => (
              <li key={`${event.name}-${event.at}`} className="flex flex-col gap-0.5">
                <span className="flex items-baseline justify-between gap-3">
                  <span className="font-mono text-xs break-all">{event.name}</span>
                  <span data-numeric className="shrink-0 text-xs font-semibold">{`+${spanDurationText(Date.parse(event.at) - start)}`}</span>
                </span>
                {event.attrs && Object.keys(event.attrs).length > 0 && (
                  <span className="font-mono text-xs break-all text-muted-foreground">
                    {Object.entries(event.attrs).map(([k, v]) => `${k}=${attrText(v)}`).join(' ')}
                  </span>
                )}
              </li>
            ))}
          </ol>
        </Block>
      )}
      {links.length > 0 && (
        <Block title="Linked traces">
          <ul className="flex flex-col gap-1">
            {links.map(link => (
              <li key={`${link.traceId}-${link.spanId}`}>
                <Link to="/traces/$traceId" params={{ traceId: link.traceId }} className="font-mono text-xs break-all underline underline-offset-3">
                  {link.traceId}
                </Link>
              </li>
            ))}
          </ul>
        </Block>
      )}
    </aside>
  )
}
