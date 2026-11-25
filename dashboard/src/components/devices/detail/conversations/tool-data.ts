// the conversation cards' data: narrow mirrors of the server's own tool views
// (server/internal/agent/tools), parsed defensively — a shape the cards do not
// know falls back to the raw json figure, and every text stays text

// a tool message's content is the envelope as the model read it: {ok, data, error, target}
export interface ToolEnvelope {
  ok?: boolean
  data?: unknown
  error?: { code?: string }
  target?: string
}

export function parseEnvelope(content: string): ToolEnvelope | null {
  try {
    const v: unknown = JSON.parse(content)
    if (typeof v !== 'object' || v === null)
      return null
    return v as ToolEnvelope
  }
  catch {
    return null
  }
}

// a search or browse hit: statView (youtube_search, youtube_browse) with foundView's
// own fields (catalog_search), all optional as the model read them
export interface FoundItem {
  ref?: string
  kind?: string
  title?: string
  subtitle?: string
  channel?: { ref?: string, title?: string } | null
  durationS?: number
  upcoming?: boolean
  views?: number
  likes?: number
  plays?: number
  subscribers?: number
  videoCount?: number
  published?: string
  publishedApprox?: boolean
  artwork?: string
  year?: number
  episodes?: number
  playable?: boolean
  unavailable?: boolean
}

// a catalog item as catalog_item and screen_state embed it: the full catalog.Item,
// its artwork an object rather than statView's id string
export interface CatalogItem {
  ref?: string
  kind?: string
  title?: string
  subtitle?: string
  artwork?: { hash?: string, aspect?: string }
  durationS?: number
  show?: { names?: { ru?: string }, year?: number, episodesAired?: number, episodesTotal?: number }
  stats?: { views?: number, likes?: number, subscribers?: number, videoCount?: number }
  owner?: { title?: string }
  progress?: { positionS?: number, completed?: boolean }
}

export interface ScreenState {
  route?: string
  page?: CatalogItem
  focused?: CatalogItem
  visible?: CatalogItem[]
  query?: string
  player?: { item: CatalogItem, positionS?: number, tracks?: { id?: string, kind?: string, label?: string, selected?: boolean }[] }
}

export interface BrowseResult {
  item: FoundItem
  children: FoundItem[]
  tabs?: string[]
  sorts?: string[]
  sort?: string
  partial?: boolean
}

function asRecord(v: unknown): Record<string, unknown> | null {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
    ? v as Record<string, unknown>
    : null
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' && v !== ''
    ? v
    : undefined
}

function num(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v)
    ? v
    : undefined
}

function bool(v: unknown): boolean | undefined {
  return typeof v === 'boolean'
    ? v
    : undefined
}

function strList(v: unknown): string[] | undefined {
  if (!Array.isArray(v))
    return undefined
  const out = v.map(x => str(x)).filter((x): x is string => x !== undefined)
  return out.length > 0
    ? out
    : undefined
}

function foundItem(v: unknown): FoundItem | null {
  const r = asRecord(v)
  if (r === null)
    return null
  const channel = asRecord(r.channel)
  const item: FoundItem = {
    ref: str(r.ref),
    kind: str(r.kind),
    title: str(r.title),
    subtitle: str(r.subtitle),
    durationS: num(r.durationS),
    upcoming: bool(r.upcoming),
    views: num(r.views),
    likes: num(r.likes),
    plays: num(r.plays),
    subscribers: num(r.subscribers),
    videoCount: num(r.videoCount),
    published: str(r.published),
    publishedApprox: bool(r.publishedApprox),
    artwork: artworkId(r.artwork),
    year: num(r.year),
    episodes: num(r.episodes),
    playable: bool(r.playable),
    unavailable: bool(r.unavailable),
  }
  if (channel !== null) {
    item.channel = {
      ref: str(channel.ref),
      title: str(channel.title),
    }
  }
  return item.title === undefined && item.ref === undefined
    ? null
    : item
}

// artworkId normalizes both shapes the tools store: statView's id string, catalog.Item's {hash}
export function artworkId(v: unknown): string | undefined {
  if (typeof v === 'string') {
    return imageIdLike(v)
      ? v
      : undefined
  }
  const r = asRecord(v)
  const hash = r === null
    ? undefined
    : str(r.hash)
  return hash !== undefined && imageIdLike(hash)
    ? hash
    : undefined
}

// imageIdLike is the image proxy's id shape (images.IDPattern), checked before
// an id becomes a url
function imageIdLike(s: string): boolean {
  return /^[\w-]{24}$/.test(s)
}

function itemList(v: unknown): FoundItem[] {
  if (!Array.isArray(v))
    return []
  return v.map(foundItem).filter((x): x is FoundItem => x !== null)
}

// parseFound reads a search's answer: its list field ("items" or "results")
export function parseFound(data: unknown, field: 'items' | 'results'): FoundItem[] | null {
  const r = asRecord(data)
  if (r === null || !Array.isArray(r[field]))
    return null
  return itemList(r[field])
}

// parseBrowse reads youtube_browse's answer: its item, children and the tab it read
export function parseBrowse(data: unknown): BrowseResult | null {
  const r = asRecord(data)
  if (r === null)
    return null
  const item = foundItem(r.item)
  if (item === null || !Array.isArray(r.children))
    return null
  return {
    item,
    children: itemList(r.children),
    tabs: strList(r.tabs),
    sorts: strList(r.sorts),
    sort: str(r.sort),
    partial: bool(r.partial),
  }
}

function catalogItem(v: unknown): CatalogItem | null {
  const r = asRecord(v)
  if (r === null)
    return null
  const show = asRecord(r.show)
  const names = show === null
    ? null
    : asRecord(show.names)
  const stats = asRecord(r.stats)
  const owner = asRecord(r.owner)
  const progress = asRecord(r.progress)
  const item: CatalogItem = {
    ref: str(r.ref),
    kind: str(r.kind),
    title: str(r.title),
    subtitle: str(r.subtitle),
    durationS: num(r.durationS),
  }
  const art = artworkId(r.artwork)
  if (art !== undefined)
    item.artwork = { hash: art }
  if (show !== null) {
    item.show = {
      year: num(show.year),
      episodesAired: num(show.episodesAired),
      episodesTotal: num(show.episodesTotal),
    }
    if (names !== null) {
      item.show.names = {
        ru: str(names.ru),
      }
    }
  }
  if (stats !== null) {
    item.stats = {
      views: num(stats.views),
      likes: num(stats.likes),
      subscribers: num(stats.subscribers),
      videoCount: num(stats.videoCount),
    }
  }
  if (owner !== null) {
    item.owner = {
      title: str(owner.title),
    }
  }
  if (progress !== null) {
    item.progress = {
      positionS: num(progress.positionS),
      completed: bool(progress.completed),
    }
  }
  return item.title === undefined && item.ref === undefined
    ? null
    : item
}

// parseCatalogItem reads catalog_item's answer: the item, its children, variants and tabs
export function parseCatalogItem(data: unknown): { item: CatalogItem, children: CatalogItem[], variants: number, tabs?: string[] } | null {
  const r = asRecord(data)
  if (r === null)
    return null
  const item = catalogItem(r)
  if (item === null)
    return null
  return {
    item,
    children: Array.isArray(r.children)
      ? r.children.map(catalogItem).filter((x): x is CatalogItem => x !== null)
      : [],
    variants: Array.isArray(r.variants)
      ? r.variants.length
      : 0,
    tabs: strList(r.tabs),
  }
}

// parseScreenState reads screen_state's answer
export function parseScreenState(data: unknown): ScreenState | null {
  const r = asRecord(data)
  if (r === null)
    return null
  const player = asRecord(r.player)
  const page = catalogItem(r.page)
  const focused = catalogItem(r.focused)
  const s: ScreenState = {
    route: str(r.route),
    query: str(r.query),
    page: page ?? undefined,
    focused: focused ?? undefined,
    visible: Array.isArray(r.visible)
      ? r.visible.map(catalogItem).filter((x): x is CatalogItem => x !== null)
      : [],
  }
  if (player !== null) {
    const item = catalogItem(player.item)
    if (item !== null) {
      s.player = {
        item,
        positionS: num(player.positionS),
        tracks: Array.isArray(player.tracks)
          ? player.tracks.map((t): { id?: string, kind?: string, label?: string, selected?: boolean } => {
              const tr = asRecord(t)
              return tr === null
                ? {}
                : {
                    id: str(tr.id),
                    kind: str(tr.kind),
                    label: str(tr.label),
                    selected: bool(tr.selected),
                  }
            }).filter(t => t.id !== undefined)
          : undefined,
      }
    }
  }
  return s
}

// parseMusic reads music_search's answer: its results list
export interface MusicItem {
  kind?: string
  title?: string
  artist?: string
  owner?: string
  album?: string
  durationS?: number
  trackCount?: number
  provider?: string
}

export function parseMusic(data: unknown): MusicItem[] | null {
  const r = asRecord(data)
  if (r === null || !Array.isArray(r.results))
    return null
  return r.results.map((v): MusicItem => {
    const m = asRecord(v)
    return m === null
      ? {}
      : {
          kind: str(m.kind),
          title: str(m.title),
          artist: str(m.artist),
          owner: str(m.owner),
          album: str(m.album),
          durationS: num(m.durationS),
          trackCount: num(m.trackCount),
          provider: str(m.provider),
        }
  }).filter(m => m.title !== undefined)
}

// parseVideo reads video_search's answer: its results, each an id, a channel as a string
export function parseVideo(data: unknown): FoundItem[] | null {
  const r = asRecord(data)
  if (r === null || !Array.isArray(r.results))
    return null
  return r.results.map((v): FoundItem | null => {
    const row = asRecord(v)
    if (row === null)
      return null
    const channel = str(row.channel)
    const live = bool(row.live) === true
    return {
      ref: str(row.id),
      kind: live ? 'live' : 'video',
      title: str(row.title),
      channel: channel === undefined
        ? undefined
        : { title: channel },
      durationS: num(row.durationS),
    }
  }).filter((x): x is FoundItem => x !== null && x.title !== undefined)
}

// an alternative source as catalog_alternatives answers it
export interface AltItem {
  ref?: string
  source?: string
  title?: string
  match?: string
  episodes?: number
  voiceovers: number
  quality?: number
  current?: boolean
  unavailable?: boolean
}

// parseAlternatives reads catalog_alternatives' answer: its items
export function parseAlternatives(data: unknown): AltItem[] | null {
  const r = asRecord(data)
  if (r === null || !Array.isArray(r.items))
    return null
  return r.items.map((v): AltItem => {
    const row = asRecord(v)
    return row === null
      ? { voiceovers: 0 }
      : {
          ref: str(row.ref),
          source: str(row.source),
          title: str(row.title),
          match: str(row.match),
          episodes: num(row.episodes),
          voiceovers: Array.isArray(row.voiceovers)
            ? row.voiceovers.length
            : 0,
          quality: num(row.quality),
          current: bool(row.current),
          unavailable: bool(row.unavailable),
        }
  }).filter(a => a.title !== undefined)
}

// argsRecord reads a call's stored arguments, an object whether they were stored as one
// or as text
export function argsRecord(v: unknown): Record<string, unknown> {
  const parsed: unknown = typeof v === 'string'
    ? (() => {
        try {
          return JSON.parse(v)
        }
        catch {
          return undefined
        }
      })()
    : v
  return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
    ? parsed as Record<string, unknown>
    : {}
}

// catalogTitle prefers the show's Russian name, as the screen's cards do
export function catalogTitle(it: CatalogItem): string {
  return it.show?.names?.ru ?? it.title ?? it.ref ?? 'Untitled'
}

// catalogAsFound narrows a full catalog item into a list row's shape
export function catalogAsFound(it: CatalogItem): FoundItem {
  return {
    ref: it.ref,
    kind: it.kind,
    title: catalogTitle(it),
    subtitle: it.subtitle,
    channel: it.owner?.title !== undefined
      ? { title: it.owner.title }
      : undefined,
    durationS: it.durationS,
    views: it.stats?.views,
    subscribers: it.stats?.subscribers,
    videoCount: it.stats?.videoCount,
    artwork: it.artwork?.hash,
    year: it.show?.year,
    episodes: it.show !== undefined
      ? Math.max(it.show.episodesAired ?? 0, it.show.episodesTotal ?? 0)
      : undefined,
  }
}

// argStr reads one of a call's arguments, a non-empty string
export function argStr(args: Record<string, unknown>, key: string): string | undefined {
  const v = args[key]
  return typeof v === 'string' && v !== ''
    ? v
    : undefined
}

// plural is n things in the board's english
export function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1
    ? ''
    : 's'}`
}

// compactCount is a count as the board shows it: 133K, 1.2M
const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 })

export function countText(n: number | undefined): string {
  return n === undefined
    ? ''
    : compact.format(n)
}

// publishedText is a statView's published string, an ISO time shortened to its date;
// an approximate one is already a bare date
export function publishedText(p: string | undefined): string {
  if (p === undefined)
    return ''
  return p.length > 10
    ? p.slice(0, 10)
    : p
}

// kindAspect is an item's art shape: a video's 16:9, a title's poster, the rest square
export function kindAspect(kind: string | undefined): 'video' | 'poster' | 'square' {
  switch (kind) {
    case 'video':
    case 'live':
      return 'video'
    case 'series':
    case 'movie':
    case 'title_group':
      return 'poster'
    default:
      return 'square'
  }
}
