import type { Waterfall, WaterfallRow } from '@/lib/traces/waterfall'
import { cn } from 'cn'
import { StatusGlyph } from '@/components/data/status-glyph'
import { spanDurationText } from '@/lib/traces/format'
import { MarkLabels } from './mark-labels'
import { barColour } from './span-colour'

const pct = (share: number) => `${(share * 100).toFixed(3)}%`

// the axis' figures above the bars, and its hairlines through them
function Axis({ waterfall }: { waterfall: Waterfall }) {
  return (
    <div aria-hidden="true" className="relative h-5 text-xs text-muted-foreground">
      {waterfall.ticks.map(t => (
        <span
          key={t.ms}
          data-numeric
          className={cn('absolute top-0 whitespace-nowrap', t.at > 0.9
            ? '-translate-x-full'
            : t.at > 0 && '-translate-x-1/2')}
          style={{
            left: pct(t.at),
          }}
        >
          {spanDurationText(t.ms)}
        </span>
      ))}
    </div>
  )
}

function Grid({ waterfall }: { waterfall: Waterfall }) {
  return (
    <>
      {waterfall.ticks.map(t => (
        <span
          key={t.ms}
          aria-hidden="true"
          className="absolute inset-y-0 w-px bg-border"
          style={{
            left: pct(t.at),
          }}
        />
      ))}
    </>
  )
}

function Bar({ row }: { row: WaterfallRow }) {
  const { span } = row
  return (
    <span
      className={cn('absolute top-1/2 h-3 min-w-0.5 -translate-y-1/2 rounded-sm', barColour(span), span.live && 'opacity-75')}
      style={{
        left: pct(row.left),
        width: pct(row.width),
      }}
    >
      {span.live && (
        // the growing end of a running span
        <span aria-hidden="true" className="absolute inset-y-0 -right-1 w-1 rounded-r-sm bg-foreground motion-safe:animate-pulse" />
      )}
      {row.marks.map(mark => (
        <span
          key={`${mark.name}-${mark.offsetMs}`}
          title={`${mark.name} +${spanDurationText(mark.offsetMs)}`}
          className="absolute -top-1 -bottom-1 w-0.5 -translate-x-1/2 rounded-full bg-foreground"
          style={{
            left: row.width > 0
              ? pct((mark.at - row.left) / row.width)
              : '0%',
          }}
        />
      ))}
    </span>
  )
}

function rowLabel(row: WaterfallRow): string {
  const state = row.span.live
    ? 'running'
    : row.span.status === 'error'
      ? 'error'
      : 'ok'
  const marks = row.marks.length > 0
    ? `, ${row.marks.length} events`
    : ''
  return `${row.span.name}, ${spanDurationText(row.durationMs)} from +${spanDurationText(row.offsetMs)}, ${state}${marks}`
}

// the selected span's events under the waterfall, on its time axis: their names in lanes there, so the span rows
// keep one height and nothing below the selection moves when it changes
function EventStrip({ row, waterfall }: { row: WaterfallRow, waterfall: Waterfall }) {
  return (
    <div className="grid grid-cols-[minmax(12rem,18rem)_1fr] gap-3 border-t px-3 py-2">
      <span className="min-w-0 truncate text-xs text-muted-foreground">
        Events of
        {' '}
        <span className="font-mono text-foreground">{row.span.name}</span>
      </span>
      <span className="relative min-w-0">
        <Grid waterfall={waterfall} />
        <MarkLabels marks={row.marks} />
      </span>
    </div>
  )
}

// the trace's spans on one time axis, nested under their parents: bars by where they ran, a failed one red with its
// square, a running one growing, its events as ticks (named in the strip below for the selected span). a row is a
// button that selects
export function WaterfallView({ waterfall, selected, onSelect }: {
  waterfall: Waterfall
  selected: string | undefined
  onSelect: (spanId: string) => void
}) {
  const selectedRow = waterfall.rows.find(row => row.span.spanId === selected)
  return (
    <div className="overflow-x-auto rounded-lg border bg-card">
      <div className="min-w-[40rem]">
        <div className="sticky top-0 grid grid-cols-[minmax(12rem,18rem)_1fr] gap-3 border-b bg-card px-3 pt-2">
          <span className="text-xs font-medium text-muted-foreground">Span</span>
          <Axis waterfall={waterfall} />
        </div>
        <ul aria-label="Spans" className="py-1">
          {waterfall.rows.map((row) => {
            const isSelected = row.span.spanId === selected
            return (
              <li key={row.span.spanId}>
                <button
                  type="button"
                  aria-pressed={isSelected}
                  aria-label={rowLabel(row)}
                  onClick={() => onSelect(row.span.spanId)}
                  className={cn(
                    'grid h-(--row-height) w-full grid-cols-[minmax(12rem,18rem)_1fr] items-center gap-3 px-3 text-left hover:bg-muted/60',
                    isSelected && 'bg-accent hover:bg-accent',
                  )}
                >
                  <span className="flex min-w-0 items-center gap-1.5" style={{ paddingLeft: `${row.depth * 0.75}rem` }}>
                    {row.span.status === 'error' && <StatusGlyph shape="square" className="shrink-0 text-destructive" />}
                    <span className="truncate font-mono text-xs">{row.span.name}</span>
                    <span data-numeric className="ml-auto shrink-0 text-xs text-muted-foreground">{spanDurationText(row.durationMs)}</span>
                  </span>
                  <span className="relative h-full">
                    <Grid waterfall={waterfall} />
                    <Bar row={row} />
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
        {selectedRow && selectedRow.marks.length > 0 && <EventStrip row={selectedRow} waterfall={waterfall} />}
      </div>
    </div>
  )
}
