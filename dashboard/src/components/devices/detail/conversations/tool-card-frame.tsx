import type { ReactNode } from 'react'
import type { FoundItem } from './tool-data'
import { cn } from 'cn'
import { useState } from 'react'
import { API_PATH } from '@/api/client'
import { Tag } from '@/components/data/tag'
import { durationText } from '@/lib/format-time'
import { argStr, countText, kindAspect, plural, publishedText } from './tool-data'

// the conversation cards' shared frame: a bordered card on the drawer's stack, the tool's
// word as its title, its rows plain text over the server's own images

// Thumb is an item's art through the server's image proxy (same origin, the owner's
// cookie); a muted box stands in while there is none or it failed to load
function Thumb({ id, kind }: { id: string | undefined, kind: string | undefined }) {
  const [failed, setFailed] = useState(false)
  const aspect = kindAspect(kind)
  return (
    <div
      aria-hidden="true"
      className={cn('shrink-0 overflow-hidden rounded-md border bg-muted', aspect === 'poster'
        ? 'h-[4.5rem] w-12'
        : aspect === 'video'
          ? 'h-16 w-28'
          : 'size-12')}
    >
      {id !== undefined && !failed && (
        <img
          src={`${API_PATH}/catalog/images/${encodeURIComponent(id)}`}
          alt=""
          loading="lazy"
          className="size-full object-cover"
          onError={() => setFailed(true)}
        />
      )}
    </div>
  )
}

// joined keeps the line sparse: only the parts that are there, ' · ' between
function joined(parts: readonly (string | undefined)[]): string {
  return parts.filter((p): p is string => p !== undefined && p !== '').join(' · ')
}

// itemMeta is an item's second line: whose it is, its counts, its length, its date
function itemMeta(it: FoundItem): string {
  const mine: (string | undefined)[] = []
  switch (it.kind) {
    case 'channel':
      mine.push(it.subtitle)
      if (it.subscribers !== undefined)
        mine.push(`${countText(it.subscribers)} subscribers`)
      if (it.videoCount !== undefined)
        mine.push(`${countText(it.videoCount)} videos`)
      break
    case 'playlist':
      mine.push(it.channel?.title, it.subtitle)
      break
    default: {
      const counts: (string | undefined)[] = []
      if (it.kind === 'live')
        counts.push('Live')
      if (it.upcoming === true)
        counts.push('Upcoming')
      if (it.views !== undefined)
        counts.push(`${countText(it.views)} views`)
      else if (it.plays !== undefined)
        counts.push(`${countText(it.plays)} plays`)
      if (it.subscribers !== undefined)
        counts.push(`${countText(it.subscribers)} subscribers`)
      mine.push(it.channel?.title ?? it.subtitle, ...counts)
      if (it.durationS !== undefined && it.durationS > 0)
        mine.push(durationText(it.durationS * 1000))
      const date = it.year !== undefined && it.year > 0
        ? String(it.year)
        : publishedText(it.published)
      if (date !== '')
        mine.push(date)
      if (it.episodes !== undefined && it.episodes > 1)
        mine.push(`${it.episodes} episodes`)
      if (it.unavailable === true)
        mine.push('Unavailable')
      break
    }
  }
  return joined(mine)
}

// ItemLine is one found thing: its art, its title, its meta
export function ItemLine({ it }: { it: FoundItem }) {
  return (
    <div className="flex items-start gap-3">
      <Thumb id={it.artwork} kind={it.kind} />
      <div className="flex min-w-0 flex-col gap-0.5">
        <p className="text-sm leading-snug font-medium break-words line-clamp-2">{it.title ?? it.ref ?? 'Untitled'}</p>
        {itemMeta(it) !== '' && <p className="text-xs text-muted-foreground">{itemMeta(it)}</p>}
      </div>
    </div>
  )
}

// ToolCard is the drawer's card: a header line (the tool's word and its context) over
// a body, with the stored json behind a disclosure at the bottom
export function ToolCard({ title, meta, children, raw }: {
  title: ReactNode
  meta?: ReactNode
  children?: ReactNode
  raw?: string
}) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border bg-card p-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <p className="text-sm font-medium">{title}</p>
        {meta !== undefined && <p className="text-xs text-muted-foreground">{meta}</p>}
      </div>
      {children}
      {raw !== undefined && (
        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer">Raw JSON</summary>
          <pre className="mt-1 overflow-auto rounded-md border bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">{raw}</pre>
        </details>
      )}
    </div>
  )
}

// Rows is the cards' list: at most limit rows, the rest counted below. Its rows carry
// no keys of their own: the row's place is its key
export function Rows({ items, limit = 6 }: { items: readonly ReactNode[], limit?: number }) {
  const rest = items.length - limit
  return (
    <ul className="flex flex-col gap-2">
      {items.slice(0, limit).map((row, i) => (
        // eslint-disable-next-line react/no-array-index-key -- a stored answer's rows never reorder
        <li key={`row-${i}`} className="list-none">{row}</li>
      ))}
      {rest > 0 && (
        <li className="text-xs text-muted-foreground">
          +
          {rest}
          {' '}
          more
        </li>
      )}
    </ul>
  )
}

// StateLine is a one-line outcome: a word in ink and its state word beside it
export function StateLine({ children }: { children: ReactNode }) {
  return <p className="text-sm">{children}</p>
}

// Chips is the call line's filters, each a tag: only the ones the model set
export function Chips({ args, keys }: { args: Record<string, unknown>, keys: readonly string[] }) {
  const chips = keys.map(k => [
    k,
    argStr(args, k),
  ] as const).filter(([, v]) => v !== undefined)
  if (chips.length === 0)
    return null
  return (
    <span className="flex flex-wrap gap-1">
      {chips.map(([k, v]) => <Tag key={k} mono>{`${k} ${v}`}</Tag>)}
    </span>
  )
}

// Ref is an opaque catalog ref in mono, as ids are set
export function Ref({ value }: { value: string }) {
  return <code className="font-mono text-xs text-muted-foreground break-all">{value}</code>
}

// TextRow is a plain titled row where no art exists: alternatives, music
export function TextRow({ title, meta }: { title: string, meta: string }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <p className="text-sm leading-snug font-medium break-words line-clamp-2">{title}</p>
      {meta !== '' && <p className="text-xs text-muted-foreground">{meta}</p>}
    </div>
  )
}

// FoundCard is a search's answer: its rows under a count
export function FoundCard({ label, items, raw }: { label: string, items: readonly FoundItem[], raw: string }) {
  return (
    <ToolCard
      title={label}
      meta={items.length > 0
        ? plural(items.length, 'result')
        : 'No results'}
      raw={raw}
    >
      {items.length > 0 && (
        <Rows items={items.map((it, i) => (
          // eslint-disable-next-line react/no-array-index-key -- a stored answer's rows never reorder
          <ItemLine key={`${it.ref ?? it.title ?? ''}-${i}`} it={it} />
        ))}
        />
      )}
    </ToolCard>
  )
}
