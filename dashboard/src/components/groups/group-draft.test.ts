import type { GroupDraft } from './group-draft'
import type { GroupView } from '@/api/gen'
import { describe, expect, it } from 'vitest'
import { ApiError } from '@/api/error'
import { checkDraft, draftOf, memberOf, NEW_GROUP, requestOf, toggleMember } from './group-draft'
import { groupRefusal } from './group-refusal'

const PAIR: GroupView = {
  id: 'g1',
  name: 'Living room',
  kind: 'stereo_pair',
  active: false,
  leaderId: 'd2',
  clockLeaderId: 'd2',
  preferredLeader: '',
  latencyMs: 600,
  bitrateKbps: 320,
  waitMs: 2000,
  pcm: true,
  autoLatency: true,
  autoBitrate: true,
  autoWait: true,
  followVideo: true,
  trackingEnabled: false,
  tracking: { state: 'off', members: [] },
  createdAt: '2026-11-15T10:00:00Z',
  updatedAt: '2026-11-15T10:00:00Z',
  // the right speaker first: the draft finds each side by its channel
  members: [
    {
      deviceId: 'd2',
      channel: 'right',
      trimMs: -20,
      trimUs: -20000,
      online: true,
    },
    {
      deviceId: 'd1',
      channel: 'left',
      trimMs: 0,
      trimUs: 0,
      online: true,
    },
  ],
}

const NAMES = new Map([
  ['d1', 'Kitchen'],
  ['d2', 'Hall'],
  ['d3', 'Study'],
])

function group(...ids: string[]): GroupDraft {
  return {
    ...NEW_GROUP,
    name: 'Downstairs',
    members: ids.map(id => memberOf(id)),
  }
}

describe('a group\'s draft', () => {
  it('reads a pair\'s sides by channel and sends the left first, keeping what the form does not change', () => {
    const draft = draftOf(PAIR)
    expect(draft.left).toEqual({ deviceId: 'd1', channel: 'left', trimMs: '' })
    expect(draft.right).toEqual({ deviceId: 'd2', channel: 'right', trimMs: '-20' })
    expect(requestOf(draft)).toEqual({
      name: 'Living room',
      kind: 'stereo_pair',
      latencyMs: 600,
      bitrateKbps: 320,
      waitMs: 2000,
      pcm: true,
      autoLatency: true,
      autoBitrate: true,
      autoWait: true,
      followVideo: true,
      members: [
        { deviceId: 'd1', channel: 'left', trimMs: 0 },
        { deviceId: 'd2', channel: 'right', trimMs: -20 },
      ],
    })
  })

  it('leaves the server\'s defaults to it, trims the name and drops a leader no longer a member', () => {
    const draft = {
      ...group('d1', 'd2'),
      name: '  Downstairs ',
      preferredLeader: 'd3',
    }
    expect(requestOf(draft)).toEqual({
      name: 'Downstairs',
      kind: 'group',
      latencyMs: 1000,
      pcm: false,
      autoLatency: true,
      autoBitrate: true,
      autoWait: true,
      followVideo: true,
      members: [
        { deviceId: 'd1', channel: 'both', trimMs: 0 },
        { deviceId: 'd2', channel: 'both', trimMs: 0 },
      ],
    })
    expect(requestOf({ ...draft, preferredLeader: 'd2' }).preferredLeader).toBe('d2')
  })

  it('keeps the pick order, and a leader taken out leads no more', () => {
    let draft = toggleMember(group(), 'd3', true)
    draft = toggleMember(draft, 'd1', true)
    draft = toggleMember({ ...draft, preferredLeader: 'd3' }, 'd3', true)
    expect(draft.members.map(m => m.deviceId)).toEqual(['d3', 'd1'])
    draft = toggleMember(draft, 'd3', false)
    expect(draft.members.map(m => m.deviceId)).toEqual(['d1'])
    expect(draft.preferredLeader).toBe('')
  })

  it('switching the kind keeps both flows\' picks', () => {
    const draft = {
      ...group('d1', 'd2', 'd3'),
      kind: 'stereo_pair' as const,
      left: memberOf('d1', 'left'),
      right: memberOf('d2', 'right'),
    }
    expect(requestOf(draft).members).toHaveLength(2)
    expect(requestOf({ ...draft, kind: 'group' }).members).toHaveLength(3)
  })
})

describe('what the form refuses before sending', () => {
  it('passes a whole group', () => {
    expect(checkDraft(group('d1', 'd2'), ['Upstairs'])).toEqual({})
  })

  it('names each field the server would refuse', () => {
    const draft: GroupDraft = {
      ...group('d1'),
      name: 'upstairs',
      latencyMs: '150',
      bitrateKbps: '600',
      waitMs: '1.5',
      members: [
        { deviceId: 'd1', channel: 'both', trimMs: '501' },
      ],
    }
    expect(checkDraft(draft, ['Upstairs'])).toEqual({
      'name': 'Another group has this name.',
      'latencyMs': 'At least 200.',
      'bitrateKbps': 'At most 510.',
      'waitMs': 'Must be a whole number.',
      'members': 'Pick at least 2 devices.',
      'members/0/trimMs': 'At most 500.',
    })
    expect(checkDraft({ ...draft, name: ' ', latencyMs: '' }, []).name).toBe('Required.')
    expect(checkDraft({ ...draft, latencyMs: '' }, []).latencyMs).toBe('Required.')
    expect(checkDraft(group('1', '2', '3', '4', '5', '6', '7', '8', '9'), []).members).toBe('At most 8 devices.')
  })

  it('wants a pair\'s two sides, and two different speakers', () => {
    const pair: GroupDraft = { ...NEW_GROUP, name: 'Pair', kind: 'stereo_pair' }
    expect(checkDraft(pair, [])).toEqual({
      'members/0/deviceId': 'Pick the left speaker.',
      'members/1/deviceId': 'Pick the right speaker.',
    })
    expect(checkDraft({
      ...pair,
      left: memberOf('d1', 'left'),
      right: memberOf('d1', 'right'),
    }, [])).toEqual({
      'members/1/deviceId': 'Pick another speaker than the left one.',
    })
  })
})

describe('where the server\'s refusal lands', () => {
  const refused = (status: number, error: string, path?: string) => new ApiError(status, { error, path })
  const pair = draftOf(PAIR)

  it.each([
    [
      refused(409, 'name_taken', '/body/name'),
      group('d1', 'd2'),
      { field: 'name', message: 'Another group has this name (name_taken).' },
    ],
    [
      refused(422, 'invalid', '/body/latencyMs'),
      group('d1', 'd2'),
      { field: 'latencyMs', message: 'The server refused this value (invalid).' },
    ],
    [
      refused(422, 'invalid', '/body/members/1/trimMs'),
      group('d1', 'd2'),
      { field: 'members/1/trimMs', message: 'The server refused this value (invalid).' },
    ],
    [
      refused(409, 'in_group', '/body/members/1/deviceId'),
      group('d1', 'd2'),
      { field: 'members', message: '«Hall» is in another group: take it out there first (in_group).' },
    ],
    [
      refused(422, 'unknown_device', '/body/members/0'),
      group('d3', 'd2'),
      { field: 'members', message: '«Study» is no longer a device (unknown_device).' },
    ],
    [
      refused(409, 'in_group', '/body/members'),
      group('d1', 'd2'),
      { field: 'members', message: 'A device is in another group now (in_group).' },
    ],
    // a pair's sides: members/0 is the left, members/1 the right
    [
      refused(409, 'in_group', '/body/members/1/deviceId'),
      pair,
      { field: 'members/1/deviceId', message: '«Hall» is in another group: take it out there first (in_group).' },
    ],
    [
      refused(422, 'invalid', '/body/members/1/channel'),
      pair,
      { field: 'members/1/deviceId', message: 'The server refused this value (invalid).' },
    ],
    [
      refused(409, 'stereo_pair'),
      pair,
      { message: 'A stereo pair keeps its two speakers (stereo_pair).' },
    ],
    [
      refused(404, 'not_found'),
      pair,
      { message: 'This group is gone: another change deleted it (not_found).' },
    ],
    [
      refused(422, 'invalid', '/body/somethingNew'),
      pair,
      { message: 'The server answered 422 invalid at /body/somethingNew.' },
    ],
  ])('%s', (error, draft, expected) => {
    expect(groupRefusal(error, draft, NAMES)).toEqual(expected)
  })
})

describe('a calibrated trim', () => {
  const calibrated: GroupView = {
    id: 'g',
    name: 'Pair',
    kind: 'group',
    active: true,
    leaderId: 'd1',
    clockLeaderId: 'd1',
    preferredLeader: '',
    latencyMs: 1000,
    bitrateKbps: 256,
    waitMs: 3000,
    pcm: false,
    autoLatency: true,
    autoBitrate: true,
    autoWait: true,
    followVideo: true,
    trackingEnabled: false,
    tracking: { state: 'off', members: [] },
    createdAt: '2026-11-15T10:00:00Z',
    updatedAt: '2026-11-15T10:00:00Z',
    members: [
      {
        deviceId: 'd1',
        channel: 'both',
        trimMs: 12,
        trimUs: 12345,
        online: true,
      },
      {
        deviceId: 'd2',
        channel: 'both',
        trimMs: -3,
        trimUs: -3000,
        online: true,
      },
    ],
  }

  it('is sent back to the microsecond while its rounded trim is left as shown', () => {
    const draft = draftOf(calibrated)
    expect(draft.members[0]!.trimMs).toBe('12')
    expect(requestOf(draft).members).toEqual([
      {
        deviceId: 'd1',
        channel: 'both',
        trimMs: 12,
        trimUs: 12345,
      },
      { deviceId: 'd2', channel: 'both', trimMs: -3 },
    ])
  })

  it('gives way to a trim typed over it', () => {
    const draft = draftOf(calibrated)
    draft.members[0] = { ...draft.members[0]!, trimMs: '20' }
    expect(requestOf(draft).members?.[0]).toEqual({ deviceId: 'd1', channel: 'both', trimMs: 20 })
  })
})
