import type { ModuleInfo } from '@/api/gen'

// a catalog as list-provider-modules answers it (by kind's name, then id), the schemas copied from the server's modules
// with their keys in each module's order, as the server sends them
export const CATALOG: ModuleInfo[] = [
  {
    id: 'openai-compatible',
    kind: 'embed',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['baseUrl', 'model'],
      properties: {
        baseUrl: { type: 'string', pattern: '^https://.' },
        model: { type: 'string', minLength: 1 },
      },
    },
    secrets: [
      { name: 'apiKey', required: false },
    ],
  },
  {
    id: 'homeassistant',
    kind: 'homeassistant',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['baseUrl'],
      properties: {
        baseUrl: { type: 'string', pattern: '^https?://', maxLength: 512 },
      },
    },
    secrets: [
      { name: 'token', required: true },
    ],
  },
  {
    id: 'openrouter',
    kind: 'llm',
    configSchema: { type: 'object', additionalProperties: false },
    secrets: [
      { name: 'apiKey', required: true },
    ],
  },
  {
    id: 'otlp-http',
    kind: 'otlp',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      required: ['url'],
      properties: {
        url: { type: 'string', pattern: '^https://.', maxLength: 2048 },
        timeoutMs: { type: 'integer', minimum: 1000, maximum: 30000 },
      },
    },
    secrets: [
      { name: 'authorization', required: false },
    ],
  },
  {
    id: 'deepgram-flux',
    kind: 'stt',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      properties: {
        model: { type: 'string', minLength: 1 },
        eotThreshold: { type: 'number', exclusiveMinimum: 0, maximum: 1 },
      },
    },
    secrets: [
      { name: 'apiKey', required: true },
    ],
  },
  {
    id: 'deepgram-aura',
    kind: 'tts',
    configSchema: { type: 'object', additionalProperties: false },
    secrets: [
      { name: 'apiKey', required: true },
    ],
  },
  {
    id: 'open-meteo',
    kind: 'weather',
    configSchema: { type: 'object', additionalProperties: false },
    secrets: [],
  },
  // a kind newer than the page: listed after the known ones, under its own name
  {
    id: 'calendar-ical',
    kind: 'calendar',
    configSchema: { type: 'object', additionalProperties: false },
    secrets: [],
  },
]
