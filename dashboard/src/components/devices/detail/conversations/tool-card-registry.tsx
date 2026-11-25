import type { ReactNode } from 'react'
import type { TurnCall, TurnMessage } from '@/api/gen'
import { Tag } from '@/components/data/tag'
import { durationText } from '@/lib/format-time'
import { Chips, FoundCard, ItemLine, Ref, Rows, StateLine, TextRow, ToolCard } from './tool-card-frame'
import {
  argsRecord,
  argStr,
  catalogAsFound,
  catalogTitle,
  parseAlternatives,
  parseBrowse,
  parseCatalogItem,
  parseEnvelope,
  parseFound,
  parseMusic,
  parseScreenState,
  parseVideo,
  plural,
} from './tool-data'

// the conversation drawer's tool cards: the screen, catalog, youtube, video and music
// tools drawn as what they found or did, the stored json behind a disclosure. A tool
// not named here keeps its raw json figures, as before.

// LABELS names every tool the cards know
const LABELS: Readonly<Record<string, string>> = {
  youtube_search: 'YouTube search',
  youtube_browse: 'YouTube browse',
  catalog_search: 'Catalog search',
  catalog_item: 'Catalog item',
  catalog_alternatives: 'Catalog alternatives',
  catalog_play: 'Catalog play',
  screen_state: 'Screen state',
  screen_navigate: 'Screen navigate',
  video_search: 'Video search',
  video_play: 'Video play',
  music_search: 'Music search',
  music_play: 'Music play',
}

export function toolLabel(name: string): string | undefined {
  return LABELS[name]
}

// a query as the call line quotes it, «» around it
function quoted(s: string | undefined): string | undefined {
  return s === undefined
    ? undefined
    : `«${s}»`
}

// a call's summary: its main line and the argument keys shown as chips
interface CallView {
  line: ReactNode
  keys: readonly string[]
}

// summarizeCall is one tool's call line, null for a tool the cards do not know
function summarizeCall(name: string, args: Record<string, unknown>): CallView | null {
  switch (name) {
    case 'youtube_search':
      return {
        line: quoted(argStr(args, 'query')),
        keys: [
          'type',
          'sort',
          'uploaded',
          'duration',
          'limit',
        ],
      }
    case 'catalog_search':
      return {
        line: quoted(argStr(args, 'query')),
        keys: ['kind'],
      }
    case 'video_search':
    case 'music_search':
      return {
        line: quoted(argStr(args, 'query')),
        keys: ['kind', 'limit'],
      }
    case 'youtube_browse':
      return {
        line: <Ref value={argStr(args, 'ref') ?? '?'} />,
        keys: ['tab', 'sort', 'query'],
      }
    case 'catalog_item':
      return {
        line: <Ref value={argStr(args, 'ref') ?? '?'} />,
        keys: ['episode', 'group'],
      }
    case 'catalog_alternatives':
      return { line: <Ref value={argStr(args, 'ref') ?? '?'} />, keys: [] }
    case 'catalog_play':
      return {
        line: <Ref value={argStr(args, 'ref') ?? '?'} />,
        keys: ['episode', 'from', 'device'],
      }
    case 'screen_state':
      return {
        line: null,
        keys: ['device'],
      }
    case 'screen_navigate':
      return {
        line: argStr(args, 'to') !== undefined
          ? `to ${argStr(args, 'to')}`
          : null,
        keys: ['ref', 'query', 'device'],
      }
    case 'video_play':
    case 'music_play':
      return {
        line: <Ref value={argStr(args, 'id') ?? '?'} />,
        keys: ['when', 'device'],
      }
    default:
      return null
  }
}

// CallLine is the call in the drawer: the tool's word, what it was asked for, its
// filters, the raw arguments behind a disclosure so the stored truth stays one click
// away. Null keeps the raw json figure (a tool the cards do not know)
export function toolCallSummary({ call }: { call: TurnCall }): ReactNode | null {
  const args = argsRecord(call.arguments)
  const s = summarizeCall(call.name, args)
  if (s === null)
    return null
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-sm">
        <span className="font-mono text-xs text-muted-foreground">{call.name}</span>
        {s.line}
        <Chips args={args} keys={s.keys} />
      </div>
      {call.arguments !== undefined && (
        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer">Raw arguments</summary>
          <pre className="mt-1 overflow-auto rounded-md border bg-muted p-2 font-mono text-xs whitespace-pre-wrap wrap-anywhere">
            {typeof call.arguments === 'string'
              ? call.arguments
              : JSON.stringify(call.arguments, null, 2)}
          </pre>
        </details>
      )}
    </div>
  )
}

// joinedMeta drops the parts that are not there
function joinedMeta(parts: readonly (string | undefined)[]): string {
  return parts.filter((p): p is string => p !== undefined && p !== '').join(' · ')
}

// renderResult is one tool's result card from its stored envelope; null keeps the raw
// figure (an unknown tool, or a shape the cards cannot read)
function renderResult(name: string, message: TurnMessage, args: Record<string, unknown>): ReactNode | null {
  const label = toolLabel(name)
  if (label === undefined)
    return null
  const env = parseEnvelope(message.content)
  if (env === null)
    return null
  const code = env.error?.code ?? message.code
  if (code !== undefined && code !== '') {
    const s = summarizeCall(name, args)
    return (
      <ToolCard
        title={label}
        meta={<code data-tone="danger" className="font-mono text-destructive">{code}</code>}
        raw={message.content}
      >
        {s !== null && (
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-sm">
            <span className="font-mono text-xs text-muted-foreground">{name}</span>
            {s.line}
            <Chips args={args} keys={s.keys} />
          </div>
        )}
      </ToolCard>
    )
  }
  switch (name) {
    case 'youtube_search':
    case 'catalog_search': {
      const items = parseFound(env.data, 'items')
      return items === null
        ? null
        : <FoundCard label={label} items={items} raw={message.content} />
    }
    case 'video_search': {
      const items = parseVideo(env.data)
      return items === null
        ? null
        : <FoundCard label={label} items={items} raw={message.content} />
    }
    case 'youtube_browse': {
      const b = parseBrowse(env.data)
      if (b === null)
        return null
      const meta = joinedMeta([
        b.children.length > 0 && b.tabs !== undefined
          ? b.tabs[b.tabs.length - 1]
          : undefined,
        b.sort,
        b.partial === true
          ? 'partial'
          : undefined,
        plural(b.children.length, 'item'),
      ])
      return (
        <ToolCard title={label} meta={meta} raw={message.content}>
          <Rows items={[
            <ItemLine key="item" it={b.item} />,
            ...b.children.map((it, i) => (
              // eslint-disable-next-line react/no-array-index-key -- a stored answer's rows never reorder
              <ItemLine key={`${it.ref ?? it.title ?? ''}-${i}`} it={it} />
            )),
          ]}
          />
        </ToolCard>
      )
    }
    case 'catalog_item': {
      const v = parseCatalogItem(env.data)
      if (v === null)
        return null
      return (
        <ToolCard
          title={label}
          meta={v.variants > 0
            ? `${plural(v.children.length, 'item')} · ${plural(v.variants, 'variant')}`
            : plural(v.children.length, 'item')}
          raw={message.content}
        >
          <Rows items={[
            <ItemLine key="item" it={catalogAsFound(v.item)} />,
            ...v.children.map((it, i) => (
              // eslint-disable-next-line react/no-array-index-key -- a stored answer's rows never reorder
              <ItemLine key={`${it.ref ?? it.title ?? ''}-${i}`} it={catalogAsFound(it)} />
            )),
          ]}
          />
        </ToolCard>
      )
    }
    case 'catalog_alternatives': {
      const items = parseAlternatives(env.data)
      if (items === null)
        return null
      return (
        <ToolCard title={label} meta={plural(items.length, 'source')} raw={message.content}>
          {items.length > 0 && (
            <Rows items={items.map((it, i) => (
              <TextRow
                // eslint-disable-next-line react/no-array-index-key -- a stored answer's rows never reorder
                key={`${it.ref ?? it.title ?? ''}-${i}`}
                title={`${it.title ?? 'Untitled'}${it.unavailable === true
                  ? ' · Unavailable'
                  : ''}`}
                meta={joinedMeta([
                  it.source,
                  it.match,
                  it.episodes !== undefined && it.episodes > 0
                    ? `${it.episodes} episodes`
                    : undefined,
                  it.quality !== undefined && it.quality > 0
                    ? `${it.quality}p`
                    : undefined,
                  it.voiceovers > 0
                    ? `${it.voiceovers} voiceovers`
                    : undefined,
                  it.current === true
                    ? 'current'
                    : undefined,
                ])}
              />
            ))}
            />
          )}
        </ToolCard>
      )
    }
    case 'catalog_play': {
      const playing = env.ok === true
      const episode = argStr(args, 'episode')
      return (
        <ToolCard
          title={label}
          meta={joinedMeta([
            playing ? undefined : 'not playing',
            episode !== undefined
              ? `episode ${episode}`
              : undefined,
          ]) || (playing ? 'Playing' : undefined)}
          raw={message.content}
        >
          <StateLine>{playing ? 'Started on the screen' : 'Did not start'}</StateLine>
        </ToolCard>
      )
    }
    case 'screen_state': {
      const s = parseScreenState(env.data)
      if (s === null)
        return null
      const page = s.page !== undefined
        ? catalogTitle(s.page)
        : undefined
      const focused = s.focused !== undefined
        ? catalogTitle(s.focused)
        : undefined
      const visible = s.visible ?? []
      return (
        <ToolCard
          title={label}
          meta={s.route !== undefined
            ? <Tag mono>{s.route}</Tag>
            : undefined}
          raw={message.content}
        >
          <div className="flex flex-col gap-1 text-sm">
            {s.query !== undefined && <p className="text-xs text-muted-foreground">{`query «${s.query}»`}</p>}
            {page !== undefined && <p>{`Page: ${page}`}</p>}
            {focused !== undefined && focused !== page && <p className="text-muted-foreground">{`Focused: ${focused}`}</p>}
            {visible.length > 0 && (
              <p className="text-muted-foreground">
                {`Visible: ${visible.slice(0, 3).map(catalogTitle).join(' · ')}${visible.length > 3
                  ? ` · +${visible.length - 3} more`
                  : ''}`}
              </p>
            )}
            {s.player !== undefined && (
              <p>
                {`Playing: ${catalogTitle(s.player.item)}`}
                {s.player.positionS !== undefined && s.player.positionS > 0
                  ? ` · ${durationText(s.player.positionS * 1000)}`
                  : ''}
                {s.player.tracks !== undefined && s.player.tracks.length > 0
                  ? ` · ${s.player.tracks.length} tracks`
                  : ''}
              </p>
            )}
          </div>
        </ToolCard>
      )
    }
    case 'screen_navigate':
      return (
        <ToolCard title={label} raw={message.content}>
          <StateLine>{`Navigated to ${argStr(args, 'to') ?? 'the screen'}`}</StateLine>
        </ToolCard>
      )
    case 'video_play': {
      const d = env.data as { height?: unknown, live?: unknown } | null | undefined
      const meta = joinedMeta([
        typeof d?.live === 'boolean' && d.live
          ? 'live'
          : undefined,
        typeof d?.height === 'number' && d.height > 0
          ? `${d.height}p`
          : undefined,
      ])
      return (
        <ToolCard title={label} meta={meta} raw={message.content}>
          <StateLine>Started on the screen</StateLine>
        </ToolCard>
      )
    }
    case 'music_play': {
      const d = env.data as { playing?: unknown, queued?: unknown, radio?: unknown, songs?: unknown } | null | undefined
      const state = d?.radio === true
        ? 'Radio started'
        : d?.queued === true
          ? 'Queued'
          : 'Playing'
      return (
        <ToolCard
          title={label}
          meta={typeof d?.songs === 'number' && d.songs > 0
            ? plural(d.songs, 'song')
            : undefined}
          raw={message.content}
        >
          <StateLine>{state}</StateLine>
        </ToolCard>
      )
    }
    case 'music_search': {
      const items = parseMusic(env.data)
      if (items === null)
        return null
      return (
        <ToolCard
          title={label}
          meta={items.length > 0
            ? plural(items.length, 'result')
            : 'No results'}
          raw={message.content}
        >
          {items.length > 0 && (
            <Rows items={items.map((m, i) => (
              <TextRow
                // eslint-disable-next-line react/no-array-index-key -- a stored answer's rows never reorder
                key={`${m.title ?? ''}-${i}`}
                title={m.title ?? 'Untitled'}
                meta={joinedMeta([
                  m.artist ?? m.owner,
                  m.album,
                  m.provider,
                  m.trackCount !== undefined && m.trackCount > 0
                    ? `${m.trackCount} tracks`
                    : undefined,
                  m.durationS !== undefined && m.durationS > 0
                    ? durationText(m.durationS * 1000)
                    : undefined,
                ])}
              />
            ))}
            />
          )}
        </ToolCard>
      )
    }
  }
  return null
}

// ToolResultView is a tool result as its card; args are the call's, for the context a
// failure or a play needs. Null keeps the raw json figure
export function toolResultView({ name, message, args }: { name: string, message: TurnMessage, args: unknown }): ReactNode | null {
  return renderResult(name, message, argsRecord(args))
}
