import type { WakeWordView } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { Stamp } from '@/components/data/stamp'
import { WakeWordStatus } from './wake-word-status'

function percent(v: number | undefined): string {
  return v === undefined
    ? ''
    : `${Math.round(v * 100)}%`
}

// a false detection rate as the owner reads it
export function falseText(perHour: number | undefined): string {
  if (perHour === undefined)
    return ''
  return perHour === 0
    ? 'None heard'
    : `${perHour.toFixed(1)} an hour`
}

export function wakeWordColumns(timeZone: string | null): readonly Column<WakeWordView>[] {
  return [
    {
      id: 'word',
      header: 'Word',
      sortValue: w => w.word,
      cell: w => <span className="font-medium">{w.word}</span>,
    },
    {
      id: 'status',
      header: 'Status',
      cell: w => <WakeWordStatus word={w} />,
    },
    {
      id: 'source',
      header: 'Source',
      cell: w => w.source === 'trained'
        ? (w.datasetKept
            ? 'Trained, takes kept'
            : 'Trained')
        : 'Uploaded',
      className: 'max-md:hidden',
    },
    {
      id: 'recall',
      header: 'Recall',
      align: 'end',
      cell: w => percent(w.recall),
      className: 'max-lg:hidden',
    },
    {
      id: 'false',
      header: 'False wakes',
      cell: w => falseText(w.falsePerHour),
      className: 'max-lg:hidden',
    },
    {
      id: 'threshold',
      header: 'Threshold',
      align: 'end',
      cell: w => w.threshold === undefined
        ? ''
        : <span className="font-mono text-xs">{w.threshold.toFixed(2)}</span>,
      className: 'max-xl:hidden',
    },
    {
      id: 'created',
      header: 'Added',
      sortValue: w => w.createdAt,
      firstSort: 'desc',
      cell: w => <Stamp at={w.createdAt} timeZone={timeZone} />,
      className: 'max-sm:hidden',
    },
  ]
}
