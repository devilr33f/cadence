import type { SpanView } from '@/api/gen'
import { useMemo } from 'react'
import { StatusGlyph } from '@/components/data/status-glyph'
import { traceFailure } from '@/lib/traces/failure'

// the trace's failure in one place: the deepest failed span, the failed spans above it, and why in codes; the span's
// name opens it in the panel
export function FailureSummary({ spans, onSelect }: { spans: readonly SpanView[], onSelect: (spanId: string) => void }) {
  const failure = useMemo(() => traceFailure(spans), [spans])
  if (!failure)
    return null
  const { span, facts, path } = failure
  return (
    <section aria-label="Why the trace failed" className="mb-6 flex flex-col gap-2 rounded-lg border border-destructive/40 bg-destructive-surface px-4 py-3">
      <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-sm">
        <StatusGlyph shape="square" className="shrink-0 self-center text-destructive" />
        <span className="font-medium">Error</span>
        {path.length > 0 && <span className="font-mono text-xs break-all">{`${path.join(' › ')} ›`}</span>}
        <button
          type="button"
          onClick={() => onSelect(span.spanId)}
          className="font-mono text-xs break-all underline underline-offset-3 hover:text-foreground"
        >
          {span.name}
        </button>
      </p>
      {facts.length > 0 && (
        <dl className="flex flex-wrap gap-x-5 gap-y-1 text-sm">
          {facts.map(fact => (
            <div key={fact.label} className="flex min-w-0 gap-1.5">
              <dt>{fact.label}</dt>
              <dd className="font-mono text-xs/5 break-all text-destructive">{fact.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </section>
  )
}
