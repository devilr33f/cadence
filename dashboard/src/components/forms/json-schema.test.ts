import type { FieldNode } from './json-schema'
import { describe, expect, it } from 'vitest'
import {
  ALARM_DAYS,
  ALARMS_CONFIG,
  CANCEL_ALARM,
  EQ_CONFIG,
  LIGHTS_CONFIG,
  LOGS_CONFIG,
  NO_ARGS,
  OUTPUT_CONFIG,
  OUTPUT_SCREEN_CONFIG,
  PLAYER_CONFIG,
  SET_ALARM,
  SOLOIST_CONFIG,
  SYSTEM_METRICS_CONFIG,
  URL_PLAY,
} from '@/test/device-schemas'
import { buildForm, fieldFor } from './json-schema'

// each field as kind:pointer, required ones marked with !
function outline(node: FieldNode): string[] {
  const self = `${node.kind}:${node.pointer}${node.required ? '!' : ''}`
  return node.kind === 'object'
    ? [
        self,
        ...node.fields.flatMap(outline),
      ]
    : [self]
}

describe('the renderer reads the schemas the devices declare', () => {
  it.each<[string, unknown, string[]]>([
    [
      'alarms config',
      ALARMS_CONFIG,
      ['object:', 'string:/timeZone!'],
    ],
    [
      'system.metrics config',
      SYSTEM_METRICS_CONFIG,
      ['object:', 'integer:/intervalS'],
    ],
    [
      'media.player config',
      PLAYER_CONFIG,
      ['object:', 'enum:/duckMode'],
    ],
    [
      'media.soloist config',
      SOLOIST_CONFIG,
      ['object:', 'boolean:/enabled', 'string:/deviceName'],
    ],
    [
      'display.lights config',
      LIGHTS_CONFIG,
      ['object:', 'boolean:/lights', 'boolean:/clock'],
    ],
    [
      'an alarm\'s days',
      ALARM_DAYS,
      ['object:', 'tags:/days'],
    ],
    [
      'media.url play',
      URL_PLAY,
      [
        'object:',
        'string:/url!',
        'enum:/mode',
        'object:/metadata',
        'string:/metadata/title',
        'string:/metadata/artist',
        'string:/metadata/album',
        'string:/metadata/artworkUrl',
        'integer:/startMs',
      ],
    ],
    [
      'audio.output config on a speaker (its delay trim)',
      OUTPUT_CONFIG,
      ['object:', 'integer:/delayMs'],
    ],
    [
      'audio.output config on a screen (the delay trim and the volume mode)',
      OUTPUT_SCREEN_CONFIG,
      [
        'object:',
        'integer:/delayMs',
        'object:/volume',
        'enum:/volume/mode',
      ],
    ],
    [
      'a command without arguments',
      NO_ARGS,
      ['object:'],
    ],
  ])('%s', (_, schema, expected) => {
    expect(outline(buildForm(schema, {}))).toEqual(expected)
  })

  it('keeps an enum\'s options and an integer\'s bounds', () => {
    const player = buildForm(PLAYER_CONFIG, {})
    expect(player.kind === 'object' && player.fields[0]).toMatchObject({
      kind: 'enum',
      options: ['duck', 'muffle'],
    })
    const days = buildForm(ALARM_DAYS, {})
    expect(days.kind === 'object' && days.fields[0]).toMatchObject({
      kind: 'tags',
      item: {
        kind: 'enum',
        options: [
          'mon',
          'tue',
          'wed',
          'thu',
          'fri',
          'sat',
          'sun',
        ],
      },
    })
  })
})

describe('what the renderer cannot show is edited as raw json, never dropped', () => {
  it.each<[string, unknown, string[]]>([
    [
      'patternProperties (telemetry logs sources)',
      LOGS_CONFIG,
      [
        'object:',
        'raw:/sources',
        'boolean:/kmsg',
        'boolean:/thirdParty',
        'boolean:/traces',
      ],
    ],
    [
      'an array of objects (media.eq bands)',
      EQ_CONFIG,
      ['object:', 'number:/preampDb', 'raw:/bands'],
    ],
    [
      'not at the root (set_alarm)',
      SET_ALARM,
      ['raw:'],
    ],
    [
      'oneOf and const (cancel_alarm)',
      CANCEL_ALARM,
      ['raw:'],
    ],
    [
      'a type union',
      {
        type: 'object',
        properties: {
          a: {
            type: ['string', 'null'],
          },
        },
      },
      ['object:', 'raw:/a'],
    ],
    [
      'a $ref',
      {
        type: 'object',
        properties: {
          a: { $ref: '#/$defs/a' },
        },
      },
      ['object:', 'raw:/a'],
    ],
    [
      'an enum of numbers',
      {
        type: 'object',
        properties: {
          a: {
            enum: [1, 2],
          },
        },
      },
      ['object:', 'raw:/a'],
    ],
    [
      'a pattern this browser cannot compile',
      {
        type: 'object',
        properties: {
          a: { type: 'string', pattern: '(' },
        },
      },
      ['object:', 'raw:/a'],
    ],
    [
      'no type at all',
      {
        type: 'object',
        properties: { a: {} },
      },
      ['object:', 'raw:/a'],
    ],
    [
      'a schema that is no object',
      true,
      ['raw:'],
    ],
  ])('%s', (_, schema, expected) => {
    expect(outline(buildForm(schema, {}))).toEqual(expected)
  })

  it.each<[string, unknown, unknown, string[]]>([
    [
      'a value of another type',
      ALARMS_CONFIG,
      { timeZone: 7 },
      ['object:', 'raw:/timeZone!'],
    ],
    [
      'a key the schema does not name',
      LIGHTS_CONFIG,
      { lights: true, extra: 1 },
      ['raw:'],
    ],
    [
      'an option the enum lacks',
      PLAYER_CONFIG,
      { duckMode: 'loud' },
      ['object:', 'raw:/duckMode'],
    ],
    [
      'an item the list cannot hold',
      ALARM_DAYS,
      {
        days: ['mon', 'someday'],
      },
      ['object:', 'raw:/days'],
    ],
    [
      'a fraction where an integer goes',
      SYSTEM_METRICS_CONFIG,
      { intervalS: 12.5 },
      ['object:', 'raw:/intervalS'],
    ],
    [
      'a root that is no object',
      LIGHTS_CONFIG,
      [1],
      ['raw:'],
    ],
    // a key named like one of Object.prototype's is no property the schema has
    [
      'a key named constructor',
      LIGHTS_CONFIG,
      { constructor: 1 },
      ['raw:'],
    ],
    [
      'a required name the properties lack',
      {
        type: 'object',
        properties: {},
        required: ['toString'],
      },
      {},
      ['raw:'],
    ],
  ])('%s', (_, schema, value, expected) => {
    expect(outline(buildForm(schema, value))).toEqual(expected)
  })
})

describe('the server\'s path finds its field', () => {
  const play = buildForm(URL_PLAY, {})
  const logs = buildForm(LOGS_CONFIG, {})
  const days = buildForm(ALARM_DAYS, {})

  it.each<[string, FieldNode, string, string | undefined]>([
    ['a field', play, '/url', '/url'],
    [
      'a nested field',
      play,
      '/metadata/artworkUrl',
      '/metadata/artworkUrl',
    ],
    [
      'an item of a list',
      days,
      '/days/2',
      '/days',
    ],
    [
      'inside a raw part',
      logs,
      '/sources/kernel',
      '/sources',
    ],
    [
      'the whole document',
      play,
      '',
      undefined,
    ],
    [
      'a key the form has no field for',
      play,
      '/volume',
      undefined,
    ],
    [
      'a prefix that is no segment',
      play,
      '/urlx',
      undefined,
    ],
  ])('%s', (_, root, path, expected) => {
    expect(fieldFor(root, path)?.pointer).toBe(expected)
  })

  it('reads escaped segments', () => {
    const root = buildForm({
      type: 'object',
      properties: {
        'a/b': { type: 'string' },
      },
    }, {})
    expect(fieldFor(root, '/a~1b')?.pointer).toBe('/a~1b')
  })
})
