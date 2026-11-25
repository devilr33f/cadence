import type { ReactNode } from 'react'
import type { HistoryEntry } from '@/api/gen'
import type { Column } from '@/components/data/data-table'
import { HeartIcon, MusicIcon, ThumbsDownIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { API_PATH } from '@/api/client'
import { DataTable } from '@/components/data/data-table'
import { Stamp } from '@/components/data/stamp'
import { Button } from '@/components/ui/button'
import { durationText } from '@/lib/format-time'
import { historySource } from '@/lib/history'
import { webLink } from '@/lib/music'

function Artwork({ entry }: { entry: HistoryEntry }) {
  const [failed, setFailed] = useState(false)
  const src = entry.artworkUrl || (entry.artworkHash
    ? `${API_PATH}/devices/${encodeURIComponent(entry.deviceId)}/artwork?h=${entry.artworkHash}`
    : '')
  const box = 'grid size-10 shrink-0 place-items-center overflow-hidden rounded-md border bg-muted text-muted-foreground'
  if (!src || failed) {
    return <div aria-hidden="true" className={box}><MusicIcon className="size-4" /></div>
  }
  return (
    <div aria-hidden="true" className={box}>
      <img src={src} alt="" referrerPolicy="no-referrer" className="size-full object-cover" onError={() => setFailed(true)} />
    </div>
  )
}

function Title({ entry }: { entry: HistoryEntry }) {
  const href = webLink(entry.webUrl)
  return (
    <div className="flex min-w-56 max-w-[56ch] items-center gap-3 py-1 whitespace-normal">
      <Artwork key={entry.artworkUrl ?? entry.artworkHash} entry={entry} />
      <div className="min-w-0">
        {href
          ? <a href={href} target="_blank" rel="noreferrer" className="font-medium break-words underline decoration-input underline-offset-4 hover:decoration-foreground">{entry.title || 'Untitled'}</a>
          : <p className="font-medium break-words">{entry.title || 'Untitled'}</p>}
        <p className="text-sm break-words text-muted-foreground">{[entry.artist, entry.album].filter(Boolean).join(' · ')}</p>
      </div>
    </div>
  )
}

function RatingActions({ entry, onRate, busy }: {
  entry: HistoryEntry
  onRate: (entry: HistoryEntry, feedback: HistoryEntry['feedback']) => void
  busy: boolean
}) {
  if (!entry.provider || !entry.providerSongId)
    return null
  const liked = entry.feedback === 'like'
  const disliked = entry.feedback === 'dislike'
  return (
    <div className="flex justify-end gap-1">
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={liked
          ? `Clear like for ${entry.title}`
          : `Like ${entry.title}`}
        title={liked ? 'Clear like' : 'Like and add to favorites'}
        aria-pressed={liked}
        disabled={busy}
        onClick={() => onRate(entry, liked ? 'none' : 'like')}
      >
        <HeartIcon fill={liked ? 'currentColor' : 'none'} />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={disliked
          ? `Clear dislike for ${entry.title}`
          : `Dislike ${entry.title}`}
        title={disliked ? 'Clear dislike' : 'Dislike'}
        aria-pressed={disliked}
        disabled={busy}
        onClick={() => onRate(entry, disliked ? 'none' : 'dislike')}
      >
        <ThumbsDownIcon fill={disliked ? 'currentColor' : 'none'} />
      </Button>
    </div>
  )
}

function columns(timeZone: string | null, showDevice: boolean, favorites: boolean, onRate: HistoryTableProps['onRate'], busy: boolean): Column<HistoryEntry>[] {
  const cols: Column<HistoryEntry>[] = [
    {
      id: 'started',
      header: favorites ? 'Favorited' : 'When',
      sortValue: entry => Date.parse(entry.startedAt),
      firstSort: 'desc',
      cell: entry => <Stamp at={entry.startedAt} timeZone={timeZone} />,
    },
    {
      id: 'title',
      header: 'Item',
      sortValue: entry => entry.title,
      cell: entry => <Title entry={entry} />,
    },
  ]
  if (showDevice && !favorites) {
    cols.push({
      id: 'device',
      header: 'Device',
      sortValue: entry => entry.deviceName,
      cell: entry => entry.deviceName,
      className: 'max-lg:hidden',
    })
  }
  cols.push({
    id: 'source',
    header: 'Source',
    sortValue: entry => historySource(entry),
    cell: entry => historySource(entry),
    className: 'max-md:hidden',
  })
  if (!favorites) {
    cols.push({
      id: 'played',
      header: 'Played',
      sortValue: entry => entry.playedMs,
      cell: entry => <span data-numeric>{durationText(entry.playedMs)}</span>,
      align: 'end',
    })
  }
  if (onRate) {
    cols.push({
      id: 'rating',
      header: 'Rating',
      cell: entry => <RatingActions entry={entry} onRate={onRate} busy={busy} />,
      align: 'end',
    })
  }
  return cols
}

interface HistoryTableProps {
  entries: readonly HistoryEntry[] | undefined
  timeZone: string | null
  showDevice?: boolean
  caption?: string
  status?: 'ready' | 'loading' | 'error'
  empty?: ReactNode
  error?: ReactNode
  favorites?: boolean
  busy?: boolean
  onRate?: (entry: HistoryEntry, feedback: HistoryEntry['feedback']) => void
}

export function HistoryTable({ entries, timeZone, showDevice = true, caption = 'Recently played', status = 'ready', empty = <span />, error, favorites = false, busy = false, onRate }: HistoryTableProps) {
  const cols = useMemo(() => columns(timeZone, showDevice, favorites, onRate, busy), [
    timeZone,
    showDevice,
    favorites,
    onRate,
    busy,
  ])
  return (
    <DataTable
      caption={caption}
      columns={cols}
      rows={entries}
      rowKey={entry => String(entry.id)}
      initialSort={{ column: 'started', direction: 'desc' }}
      status={status}
      empty={empty}
      error={error}
    />
  )
}
