import type { ModuleInfo } from '@/api/gen'

// the server's provider kinds (server/internal/providers/types.go) in the order the page shows them: the voice first.
// the catalog sorts by kind's name; a kind not here (one newer than the page) follows these, under its own name
export const PROVIDER_KINDS = [
  'stt',
  'tts',
  'llm',
  'weather',
  'search',
  'music',
  'player',
  'embed',
  'hermes',
  'homeassistant',
  'mcp',
  'otlp',
] as const

export type ProviderKind = (typeof PROVIDER_KINDS)[number]

export const KIND_LABELS: Record<ProviderKind, string> = {
  stt: 'Speech to text',
  tts: 'Text to speech',
  llm: 'Language model',
  weather: 'Weather',
  search: 'Web search',
  music: 'Music search',
  player: 'Music player',
  embed: 'Embeddings',
  hermes: 'Hermes tasks',
  homeassistant: 'Home Assistant',
  mcp: 'MCP server',
  otlp: 'Telemetry export',
}

function isKnownKind(kind: string): kind is ProviderKind {
  return Object.hasOwn(KIND_LABELS, kind)
}

export function kindLabel(kind: string): string {
  return isKnownKind(kind)
    ? KIND_LABELS[kind]
    : kind
}

// a known kind by the page's order; an unknown one after them all
export function kindRank(kind: string): number {
  return isKnownKind(kind)
    ? PROVIDER_KINDS.indexOf(kind)
    : PROVIDER_KINDS.length
}

// the catalog by module id, for the list's rows
export function catalogIndex(modules: readonly ModuleInfo[]): ReadonlyMap<string, ModuleInfo> {
  return new Map(modules.map(m => [m.id, m]))
}

// the modules of each kind, the kinds in the page's order (unknown ones by name), each kind's modules in the catalog's
// order (by id): the create form's select
export function modulesByKind(modules: readonly ModuleInfo[]): { kind: string, modules: ModuleInfo[] }[] {
  const kinds = [
    ...new Set(modules.map(m => m.kind)),
  ]
    .sort((a, b) => kindRank(a) - kindRank(b) || a.localeCompare(b))
  return kinds.map(kind => ({
    kind,
    modules: modules.filter(m => m.kind === kind),
  }))
}
