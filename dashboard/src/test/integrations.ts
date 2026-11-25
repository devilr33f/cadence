import type { FakeRoute } from './fake-api'
import type { AreaJson, EntityJson, KindJson, ServerView, StatusJson, ToolView, UsageJson } from '@/api/gen'
import { json, signedIn } from './fake-api'
import { DEVICES, KITCHEN, queryOf } from './household'
import { NOT_BUSY, SUBSCRIPTIONS, T3 } from './subscriptions'

export const CONNECTED: StatusJson = {
  configured: 'home',
  instance: 'home',
  connected: true,
  connectedAt: '2026-11-12T08:00:00Z',
  lastError: null,
  entities: 3,
  areas: 2,
  restartRequired: false,
  version: '2026.11.1',
}

export const NOT_CONFIGURED: StatusJson = {
  configured: null,
  instance: null,
  connected: false,
  connectedAt: null,
  lastError: null,
  entities: 0,
  areas: 0,
  restartRequired: false,
  version: null,
}

export const AREAS: AreaJson[] = [
  {
    id: 'kitchen',
    name: 'Kitchen',
    aliases: ['cooking'],
  },
  { id: 'living_room', name: 'Living room', aliases: null },
]

export const ENTITIES: EntityJson[] = [
  {
    id: 'light.kitchen',
    name: 'Kitchen light',
    domain: 'light',
    area: 'kitchen',
    areaName: 'Kitchen',
    state: 'on',
  },
  {
    id: 'switch.kettle',
    name: 'Kettle',
    domain: 'switch',
    area: 'kitchen',
    areaName: 'Kitchen',
    state: 'off',
  },
  {
    id: 'light.living_room',
    name: 'Big lamp',
    domain: 'light',
    area: 'living_room',
    areaName: 'Living room',
    state: '(text)',
  },
]

export const USAGE: UsageJson[] = [
  { entityId: 'light.kitchen', score: 12.5 },
  { entityId: 'switch.kettle', score: 3 },
]

export const KITCHEN_USAGE: UsageJson[] = [
  { entityId: 'light.living_room', score: 2 },
]

export const NOTES: ServerView = {
  instance: 'notes',
  status: 'connected',
  host: 'mcp.example:8443',
  era: 'modern',
  lastCode: '',
  lastRefresh: '2026-11-12T08:30:00Z',
  tools: {
    total: 2,
    offered: 1,
    changed: 1,
    skipped: 1,
  },
}

// a description the server cleaned of control characters but not of markup: the page must show it as text
export const HOSTILE = '[MCP notes] Echo <script>alert(1)</script> back <b>loud</b>'

export const ECHO: ToolView = {
  remote: 'echo',
  local: 'mcp_notes_echo',
  description: HOSTILE,
  hints: { readOnly: true },
  schemaBytes: 120,
  effects: 'write',
  trust: 'untrusted',
  enabledDefault: false,
  anyTurn: false,
  changed: true,
  hash: 'h2',
  ackedHash: 'h1',
  firstSeen: '2026-11-10T08:00:00Z',
  lastSeen: '2026-11-12T08:30:00Z',
  skip: 'changed',
}

export const NOTE_ADD: ToolView = {
  remote: 'note_add',
  local: 'mcp_notes_note_add',
  description: '[MCP notes] Add a note',
  hints: {},
  schemaBytes: 200,
  effects: 'write',
  trust: 'untrusted',
  enabledDefault: false,
  anyTurn: false,
  changed: false,
  hash: 'h9',
  firstSeen: '2026-11-10T08:00:00Z',
  lastSeen: '2026-11-12T08:30:00Z',
}

export const HERMES_RUNNING: KindJson[] = [
  { name: 'hermes', instance: 'hermes', configured: 'hermes' },
]

// the entities route narrows as the server does (q in the name or id, an area id, a domain), then takes the limit
export function entitiesRoute(entities: readonly EntityJson[] = ENTITIES): FakeRoute {
  return (request) => {
    const { q, area, domain, limit } = queryOf(request)
    return Response.json(entities
      .filter(e => (!q || e.name.toLowerCase().includes(q) || e.id.includes(q)) && (!area || e.area === area) && (!domain || e.domain === domain))
      .slice(0, Number(limit ?? 100)))
  }
}

const usageRoute: FakeRoute = request => Response.json(queryOf(request).device === KITCHEN
  ? KITCHEN_USAGE
  : USAGE)

// every route the integrations page reads, a household with home assistant, one mcp server and hermes running
export function integrationRoutes(over: Record<string, FakeRoute> = {}): Record<string, FakeRoute> {
  return {
    'GET /session': signedIn,
    'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
    'GET /devices': json(DEVICES),
    'GET /integrations/homeassistant': json(CONNECTED),
    'GET /integrations/homeassistant/areas': json(AREAS),
    'GET /integrations/homeassistant/entities': entitiesRoute(),
    'GET /integrations/homeassistant/usage': usageRoute,
    'GET /mcp/servers': json([NOTES]),
    'GET /mcp/servers/notes/tools': json([ECHO, NOTE_ADD]),
    'GET /tasks/kinds': json(HERMES_RUNNING),
    'GET /subscriptions': json({ subscriptions: SUBSCRIPTIONS }),
    'GET /busy': json(NOT_BUSY),
    'GET /providers': json([T3]),
    ...over,
  }
}
