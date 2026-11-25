import type { Sample } from './series'
import { useState } from 'react'
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { stampText } from '@/lib/format-time'

// a chart's points as a table, newest first: the chart's twin for whoever reads values rather than a line. it is built
// only while open, since a day's window holds hundreds of rows; where the chart averaged runs of points, so does the
// table, and says so
export function MetricTable({ title, points, averaged, format, timeZone }: {
  title: string
  points: readonly Sample[]
  averaged: boolean
  format: (value: number) => string
  timeZone: string | null
}) {
  const name = averaged
    ? `${title} points, averaged`
    : `${title} points`
  const [open, setOpen] = useState(false)
  if (points.length === 0)
    return null
  return (
    <details open={open}>
      <summary
        className="w-fit cursor-pointer text-sm text-muted-foreground hover:text-foreground"
        onClick={(event) => {
          event.preventDefault()
          setOpen(o => !o)
        }}
      >
        {averaged ? 'Points, averaged' : 'Points'}
      </summary>
      {open && (
        <div role="region" aria-label={`${name}, newest first`} tabIndex={0} className="mt-2 max-h-60 overflow-auto rounded-lg border">
          <table className="w-full border-separate border-spacing-0 text-sm">
            <caption className="sr-only">{name}</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Time</TableHead>
                <TableHead className="text-right">{averaged ? 'Average' : 'Value'}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {points.toReversed().map(point => (
                <TableRow key={point.at}>
                  <TableCell>
                    <time dateTime={new Date(point.at).toISOString()} className="font-semibold">{stampText(new Date(point.at).toISOString(), timeZone, true)}</time>
                  </TableCell>
                  <TableCell className="text-right">{format(point.value)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </table>
        </div>
      )}
    </details>
  )
}
