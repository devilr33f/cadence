import type { UserEvent } from '@testing-library/user-event'
// @vitest-environment jsdom
import type { DeviceView, GroupView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { enter, replace, setupUser } from '@/test/typing'
import { GroupPage } from './group-page'
import { GroupsPage } from './groups-page'

// jsdom has no microphone: the listener's recorder is a fake that hears nothing
const listener = vi.hoisted(() => ({
  supported: vi.fn(() => false),
  stop: vi.fn(() => new Float32Array(0)),
}))
vi.mock('@/lib/listener-recorder', () => ({
  listenerSupported: listener.supported,
  startRecorder: async () => ({
    rate: 48000,
    frameNow: () => 0,
    stop: listener.stop,
  }),
}))

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function device(id: string, name: string, model = 'yandex-pickle', extra: Partial<DeviceView> = {}): DeviceView {
  return {
    id,
    name,
    model,
    online: true,
    room: '',
    status: 'enrolled',
    lastSeenAt: '2026-11-15T10:00:00Z',
    ...extra,
  }
}

const UPSTAIRS: GroupView = {
  id: 'g1',
  name: 'Upstairs',
  kind: 'group',
  active: true,
  leaderId: 'd3',
  clockLeaderId: 'd3',
  preferredLeader: 'd3',
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
      deviceId: 'd3',
      channel: 'both',
      trimMs: 0,
      trimUs: 0,
      online: true,
      status: {
        state: 'playing',
        role: 'leader',
        synced: true,
        clockErrorUs: 0,
        queueMs: 980,
        rejoins: 0,
        latencyMs: 1000,
        placementErrorUs: 0,
        steerPpm: 0,
        underruns: 0,
        tracking: { state: 'off', members: [] },
      },
    },
    {
      deviceId: 'd4',
      channel: 'mono',
      trimMs: 15,
      trimUs: 15000,
      online: false,
      status: {
        state: 'rejoining',
        role: 'member',
        synced: false,
        clockErrorUs: 412,
        queueMs: 120,
        rejoins: 3,
        latencyMs: 1000,
        placementErrorUs: 0,
        steerPpm: 0,
        underruns: 2,
        tracking: { state: 'off', members: [] },
      },
    },
  ],
}

const PAIR: GroupView = {
  ...UPSTAIRS,
  id: 'g2',
  name: 'Hall pair',
  kind: 'stereo_pair',
  active: false,
  leaderId: '',
  clockLeaderId: '',
  preferredLeader: '',
  members: [
    {
      deviceId: 'd5',
      channel: 'left',
      trimMs: 0,
      trimUs: 0,
      online: false,
    },
    {
      deviceId: 'd6',
      channel: 'right',
      trimMs: 0,
      trimUs: 0,
      online: false,
    },
  ],
}

const DEVICES: DeviceView[] = [
  device('d1', 'Kitchen'),
  device('d2', 'Study', 'yandex-midi'),
  device('d3', 'Bedroom', 'yandex-pickle', {
    group: {
      id: 'g1',
      name: 'Upstairs',
      kind: 'group',
      leaderId: 'd3',
      members: ['d3', 'd4'],
    },
  }),
  device('d4', 'Nursery', 'yandex-pickle', {
    group: {
      id: 'g1',
      name: 'Upstairs',
      kind: 'group',
      leaderId: 'd3',
      members: ['d3', 'd4'],
    },
  }),
  device('d7', 'Garage', 'yandex-pickle', { status: 'pending' }),
]

function renderGroups(routes: Record<string, FakeRoute> = {}, path = '/groups') {
  const app = renderApp({
    path,
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /groups': json([UPSTAIRS, PAIR]),
      'GET /groups/g1': json(UPSTAIRS),
      'GET /devices': json(DEVICES),
      ...routes,
    },
    pages: {
      '/groups': GroupsPage,
      '/groups/$id': GroupPage,
    },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function rows(name: string): string[][] {
  const table = screen.getByRole('table', { name })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').map(cell => cell.textContent ?? ''))
}

function addButton(): HTMLElement {
  return screen.getAllByRole('button', { name: 'Add a group' })[0]!
}

async function openCreate(user: UserEvent) {
  await waitFor(() => expect(addButton().getAttribute('aria-disabled')).toBeNull())
  await user.click(addButton())
  return screen.findByRole('dialog', { name: 'Add a group' })
}

function messageOf(field: Element): string | null | undefined {
  const ids = field.getAttribute('aria-describedby')?.split(' ') ?? []
  const id = ids.find(i => i.endsWith('-error'))
  return id && document.getElementById(id)?.textContent
}

async function bodyOf(server: ReturnType<typeof renderApp>['server'], method: string) {
  return server.requests.find(r => r.method === method)!.clone().json()
}

describe('the groups list', () => {
  it('shows each group\'s kind, members, leader and state, by name', async () => {
    renderGroups()
    await screen.findByText('Upstairs')
    await waitFor(() => expect(rows('Groups')[1]![3]).toBe('Bedroom'))
    expect(rows('Groups')).toEqual([
      [
        'Hall pair',
        'Stereo pair',
        '2',
        'None',
        'No leader',
      ],
      [
        'Upstairs',
        'Group',
        '2',
        'Bedroom',
        'Active',
      ],
    ])
    expect(screen.getByRole('link', { name: 'Upstairs' }).getAttribute('href')).toBe('/groups/g1')
    const results = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations).toEqual([])
  })

  it('says when there is none', async () => {
    renderGroups({
      'GET /groups': json([]),
    })
    expect(await screen.findByText('No groups')).toBeTruthy()
  })
})

describe('adding a group', () => {
  it('sends the picked devices in their order with their channel and trim; one in another group cannot be picked', async () => {
    const { server, user } = renderGroups({
      'POST /groups': json({ ...UPSTAIRS, id: 'g3', name: 'Downstairs' }, 201),
    })
    const dialog = await openCreate(user)
    // only enrolled devices are offered; one in another group says which
    expect(within(dialog).queryByRole('checkbox', { name: 'Garage' })).toBeNull()
    const bedroom = within(dialog).getByRole('checkbox', { name: 'Bedroom' }) as HTMLInputElement
    expect(bedroom.disabled).toBe(true)
    expect(within(dialog).getAllByText('In Upstairs')).toHaveLength(2)

    await enter(user, within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'Downstairs')
    await user.click(within(dialog).getByRole('checkbox', { name: 'Study' }))
    await user.click(within(dialog).getByRole('checkbox', { name: 'Kitchen' }))
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Channel of Kitchen' }), 'Mono')
    await enter(user, within(dialog).getByRole('textbox', { name: 'Trim of Kitchen, ms' }), '-40')
    await replace(user, within(dialog).getByRole('textbox', { name: 'Latency, start at, ms (required)' }), '2500')
    await user.click(within(dialog).getByRole('button', { name: 'Add the group' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await bodyOf(server, 'POST')).toEqual({
      name: 'Downstairs',
      kind: 'group',
      latencyMs: 2500,
      pcm: false,
      autoLatency: true,
      autoBitrate: true,
      autoWait: true,
      followVideo: true,
      members: [
        { deviceId: 'd2', channel: 'both', trimMs: 0 },
        { deviceId: 'd1', channel: 'mono', trimMs: -40 },
      ],
    })
  })

  it('names what is missing before anything is sent', async () => {
    const { server, user } = renderGroups()
    const dialog = await openCreate(user)
    await enter(user, within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'upstairs')
    await user.click(within(dialog).getByRole('checkbox', { name: 'Kitchen' }))
    await replace(user, within(dialog).getByRole('textbox', { name: 'Latency, start at, ms (required)' }), '100')
    await user.click(within(dialog).getByRole('button', { name: 'Add the group' }))
    expect(messageOf(within(dialog).getByRole('textbox', { name: 'Name (required)' }))).toBe('Another group has this name.')
    expect(messageOf(within(dialog).getByRole('textbox', { name: 'Latency, start at, ms (required)' }))).toBe('At least 200.')
    expect(messageOf(within(dialog).getByRole('group', { name: 'Devices (required)' }))).toBe('Pick at least 2 devices.')
    expect(document.activeElement).toBe(within(dialog).getByRole('textbox', { name: 'Name (required)' }))
    expect(server.requests.some(r => r.method === 'POST')).toBe(false)
  })

  it('pairs two speakers as left and right, and says when they are different models', async () => {
    const { server, user } = renderGroups({
      'POST /groups': json(PAIR, 201),
    })
    const dialog = await openCreate(user)
    await enter(user, within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'Desk pair')
    await user.click(within(dialog).getByRole('radio', { name: 'Stereo pair' }))
    expect(within(dialog).getByText(/Pick two of the same model/)).toBeTruthy()
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Left speaker (required)' }), 'Kitchen')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Right speaker (required)' }), 'Study')
    expect(within(dialog).getByRole('status').textContent).toMatch(/different models/)
    await user.click(within(dialog).getByRole('button', { name: 'Add the group' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect((await bodyOf(server, 'POST')).members).toEqual([
      { deviceId: 'd1', channel: 'left', trimMs: 0 },
      { deviceId: 'd2', channel: 'right', trimMs: 0 },
    ])
  })

  it('puts the server\'s refusal on the side it names, by the device\'s name, and stays open', async () => {
    const { user } = renderGroups({
      'POST /groups': json({ error: 'in_group', path: '/body/members/1/deviceId' }, 409),
    })
    const dialog = await openCreate(user)
    await enter(user, within(dialog).getByRole('textbox', { name: 'Name (required)' }), 'Desk pair')
    await user.click(within(dialog).getByRole('radio', { name: 'Stereo pair' }))
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Left speaker (required)' }), 'Kitchen')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Right speaker (required)' }), 'Study')
    await user.click(within(dialog).getByRole('button', { name: 'Add the group' }))
    const right = within(dialog).getByRole('combobox', { name: 'Right speaker (required)' })
    await waitFor(() => expect(messageOf(right)).toBe('«Study» is in another group: take it out there first (in_group).'))
    await waitFor(() => expect(document.activeElement).toBe(right))
    expect(dialog.isConnected).toBe(true)
  })
})

describe('a group\'s page', () => {
  it('shows each member\'s channel, trim, link and reported sync', async () => {
    renderGroups({}, '/groups/g1')
    expect(await screen.findByRole('heading', { name: 'Upstairs' })).toBeTruthy()
    await waitFor(() => expect(rows('Members')[0]![0]).toBe('BedroomLeader'))
    expect(rows('Members')).toEqual([
      [
        'BedroomLeader',
        'Stereo',
        '0 ms',
        'Online',
        'Playing',
        'Yes',
        '0 µs',
        '980 ms',
        '0',
      ],
      [
        'Nursery',
        'Mono',
        '15 ms',
        'Offline',
        'Rejoining',
        'No',
        '412 µs',
        '120 ms',
        '3',
      ],
    ])
    expect(screen.getByText('1000 ms · auto')).toBeTruthy()
  })

  it('turns ultrasonic live tracking on from the group page', async () => {
    const update = vi.fn(json({ ...UPSTAIRS, trackingEnabled: true }))
    const { server, user } = renderGroups({ 'PUT /groups/g1': update }, '/groups/g1')
    const toggle = await screen.findByRole('switch', { name: 'Live tracking (ultrasonic)' })
    await user.click(toggle)
    await waitFor(() => expect(update).toHaveBeenCalledOnce())
    expect((await bodyOf(server, 'PUT')).trackingEnabled).toBe(true)
  })

  it('shows the active output, its delay report, and the trims kept per output', async () => {
    const [leader, member] = UPSTAIRS.members
    renderGroups({
      'GET /groups/g1': json({
        ...UPSTAIRS,
        members: [
          {
            ...leader!,
            trims: [
              { outputId: 'speaker', trimUs: 12345 },
              { outputId: 'hdmi', trimUs: -2000 },
            ],
            status: {
              ...leader!.status!,
              output: { id: 'speaker', kind: 'speaker' },
            },
          },
          {
            ...member!,
            trims: [
              { outputId: 'bt:aa:bb:cc:dd:ee:10', trimUs: -50000 },
            ],
            status: {
              ...member!.status!,
              output: { id: 'bt:aa:bb:cc:dd:ee:10', kind: 'bluetooth', delayHintUs: 145000 },
            },
          },
        ],
      }),
    }, '/groups/g1')
    await waitFor(() => expect(rows('Members')[0]![0]).toContain('Speaker'))
    expect(rows('Members')[0]![0]).toContain('Bedroom')
    expect(rows('Members')[0]![2]).toContain('12.345 ms')
    expect(rows('Members')[0]![2]).toContain('hdmi -2 ms')
    expect(rows('Members')[1]![0]).toContain('Bluetooth')
    expect(rows('Members')[1]![0]).toContain('145 ms')
    expect(rows('Members')[1]![2]).toContain('-50 ms')
    expect(rows('Members')[1]![2]).toContain('bt:aa:bb:cc:dd:ee:10')
  })

  it('shows a member\'s group error, and one the leader does not reach, in its link', async () => {
    const [leader, member] = UPSTAIRS.members
    const group: GroupView = {
      ...UPSTAIRS,
      members: [
        {
          ...leader!,
          status: {
            ...leader!.status!,
            members: [
              {
                deviceId: 'd4',
                state: 'connecting',
                connected: false,
                synced: false,
                clockErrorUs: 0,
                queueMs: 0,
                rejoins: 0,
                placementErrorUs: 0,
              },
            ],
          },
        },
        { ...member!, online: true, status: undefined },
      ],
    }
    renderGroups({
      'GET /groups/g1': json(group),
    }, '/groups/g1')
    await waitFor(() => expect(rows('Members')[1]![3]).toBe('Not connected to the leader'))
    renderGroups({
      'GET /groups/g1': json({
        ...UPSTAIRS,
        members: [
          leader!,
          {
            ...member!,
            online: true,
            status: { ...member!.status!, error: 'refused' },
          },
        ],
      }),
    }, '/groups/g1')
    await waitFor(() => expect(screen.getAllByText('Group error (refused)').length).toBe(1))
  })

  it('says who plays and who keeps the clock when they are two members', async () => {
    renderGroups({
      'GET /groups/g1': json({ ...UPSTAIRS, leaderId: 'd4', clockLeaderId: 'd3' }),
    }, '/groups/g1')
    expect(await screen.findByText('Playing from Nursery · clock: Bedroom')).toBeTruthy()
  })

  it('keeps screens apart when the group says so, and an edit leaves it so until the switch is turned', async () => {
    const { server, user } = renderGroups({
      'GET /groups/g1': json({ ...UPSTAIRS, followVideo: false }),
      'PUT /groups/g1': json(UPSTAIRS),
    }, '/groups/g1')
    const edit = await screen.findByRole('button', { name: 'Edit' })
    await waitFor(() => expect(edit.getAttribute('aria-disabled')).toBeNull())
    await user.click(edit)
    const dialog = await screen.findByRole('dialog', { name: 'Edit Upstairs' })
    const follow = within(dialog).getByRole('checkbox', { name: /Screens show the video together/ }) as HTMLInputElement
    expect(follow.checked).toBe(false)
    await user.click(within(dialog).getByRole('button', { name: 'Save the group' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect((await bodyOf(server, 'PUT')).followVideo).toBe(false)

    server.requests.length = 0
    await user.click(edit)
    const again = await screen.findByRole('dialog', { name: 'Edit Upstairs' })
    await user.click(within(again).getByRole('checkbox', { name: /Screens show the video together/ }))
    await user.click(within(again).getByRole('button', { name: 'Save the group' }))
    await waitFor(() => expect(again.isConnected).toBe(false))
    expect((await bodyOf(server, 'PUT')).followVideo).toBe(true)
  })

  it('edits the whole group, keeping what the form shows as read', async () => {
    const { server, user } = renderGroups({
      'PUT /groups/g1': json(UPSTAIRS),
    }, '/groups/g1')
    const edit = await screen.findByRole('button', { name: 'Edit' })
    await waitFor(() => expect(edit.getAttribute('aria-disabled')).toBeNull())
    await user.click(edit)
    const dialog = await screen.findByRole('dialog', { name: 'Edit Upstairs' })
    // its own members stay pickable
    expect((within(dialog).getByRole('checkbox', { name: 'Bedroom' }) as HTMLInputElement).checked).toBe(true)
    await user.click(within(dialog).getByRole('checkbox', { name: 'Kitchen' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save the group' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await bodyOf(server, 'PUT')).toEqual({
      name: 'Upstairs',
      kind: 'group',
      latencyMs: 1000,
      bitrateKbps: 256,
      waitMs: 3000,
      pcm: false,
      autoLatency: true,
      autoBitrate: true,
      autoWait: true,
      followVideo: true,
      preferredLeader: 'd3',
      members: [
        { deviceId: 'd3', channel: 'both', trimMs: 0 },
        { deviceId: 'd4', channel: 'mono', trimMs: 15 },
        { deviceId: 'd1', channel: 'both', trimMs: 0 },
      ],
    })
  })

  it('deletes it after asking, then goes back to the list', async () => {
    const deleted = vi.fn(() => new Response(null, { status: 204 }))
    const { router, user } = renderGroups({ 'DELETE /groups/g1': deleted }, '/groups/g1')
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Delete Upstairs?' })
    await user.click(within(confirm).getByRole('button', { name: 'Delete the group' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/groups'))
    expect(deleted).toHaveBeenCalledOnce()
  })

  it('calibrates after asking, showing what each speaker measured and its new trim', async () => {
    const calibrate = vi.fn(json({
      code: 'ok',
      residualUs: 0,
      applied: true,
      members: [
        {
          deviceId: 'd3',
          code: 'ok',
          offsetUs: -12345,
          confidence: 0.92,
          trimUs: 12345,
        },
        {
          deviceId: 'd4',
          code: 'ok',
          offsetUs: 12345,
          confidence: 0.81,
          trimUs: -12345,
        },
      ],
    }))
    const { user } = renderGroups({ 'POST /groups/g1/calibrate': calibrate }, '/groups/g1')
    await user.click(await screen.findByRole('button', { name: 'Calibrate' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Calibrate Upstairs?' })
    // nothing sounds before the owner confirms, and the dialog says what will
    expect(calibrate).not.toHaveBeenCalled()
    expect(within(confirm).getByText(/plays a short chirp a few times, quieter than its own/)).toBeTruthy()
    await user.click(within(confirm).getByRole('button', { name: 'Play the chirps' }))
    await waitFor(() => expect(confirm.isConnected).toBe(false))
    expect(calibrate).toHaveBeenCalledOnce()
    expect(await screen.findByRole('heading', { name: 'Calibration' })).toBeTruthy()
    expect(rows('Calibration')).toEqual([
      [
        'Bedroom',
        'Measured',
        '-12.35 ms',
        '—',
        '—',
        '—',
        '12.35 ms',
        '92%',
        '—',
      ],
      [
        'Nursery',
        'Measured',
        '12.35 ms',
        '—',
        '—',
        '—',
        '-12.35 ms',
        '81%',
        '—',
      ],
    ])
    expect(screen.getByText('Every speaker was measured and its trim set.')).toBeTruthy()
  })

  it('shows a calibration that measured too little, and a refusal inside the dialog', async () => {
    let answer = json({
      code: 'incomplete',
      residualUs: 0,
      applied: false,
      members: [
        {
          deviceId: 'd3',
          code: 'ok',
          offsetUs: 0,
          confidence: 1,
          trimUs: 0,
        },
        {
          deviceId: 'd4',
          code: 'analysis_timeout',
          offsetUs: 0,
          confidence: 0,
          trimUs: 15000,
        },
      ],
    })
    const { user } = renderGroups({
      'POST /groups/g1/calibrate': request => answer(request),
    }, '/groups/g1')
    await user.click(await screen.findByRole('button', { name: 'Calibrate' }))
    let confirm = await screen.findByRole('alertdialog', { name: 'Calibrate Upstairs?' })
    await user.click(within(confirm).getByRole('button', { name: 'Play the chirps' }))
    await waitFor(() => expect(confirm.isConnected).toBe(false))
    expect(await screen.findByText(/no trim changed/)).toBeTruthy()
    expect(rows('Calibration')[1]).toEqual([
      'Nursery',
      'Analysis took too long (analysis_timeout)',
      '—',
      '—',
      '—',
      '—',
      '15.00 ms',
      '—',
      '—',
    ])

    answer = json({ error: 'playing' }, 409)
    await user.click(screen.getByRole('button', { name: 'Calibrate' }))
    confirm = await screen.findByRole('alertdialog', { name: 'Calibrate Upstairs?' })
    await user.click(within(confirm).getByRole('button', { name: 'Play the chirps' }))
    expect(await within(confirm).findByRole('alert')).toBeTruthy()
    expect(within(confirm).getByRole('alert').textContent).toContain('playing')
  })

  it('offers this device\'s microphone only where it can record', async () => {
    const { user } = renderGroups({}, '/groups/g1')
    await user.click(await screen.findByRole('button', { name: 'Calibrate' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Calibrate Upstairs?' })
    const mine = within(confirm).getByRole('radio', { name: /This device's microphone/ }) as HTMLInputElement
    expect(mine.disabled).toBe(true)
    expect((within(confirm).getByRole('radio', { name: 'Speaker microphones' }) as HTMLInputElement).checked).toBe(true)
    expect(within(confirm).getByText('Needs a microphone, and the dashboard over https.')).toBeTruthy()
  })

  it('calibrates by listening: schedules the chirps, then posts what it heard, numbers only', async () => {
    listener.supported.mockReturnValue(true)
    const scheduled = vi.fn(json({
      code: 'scheduled',
      mode: 'listener',
      residualUs: 0,
      applied: false,
      calibrationId: 'c1',
      startsInMs: 0,
      slots: [
        { deviceId: 'd3', offsetMs: 0 },
        { deviceId: 'd4', offsetMs: 100 },
      ],
      members: [],
    }))
    const applied = vi.fn(json({
      code: 'incomplete',
      mode: 'listener',
      residualUs: 0,
      applied: false,
      members: [
        {
          deviceId: 'd3',
          code: 'missed',
          offsetUs: 0,
          confidence: 0,
          trimUs: 0,
        },
        {
          deviceId: 'd4',
          code: 'missed',
          offsetUs: 0,
          confidence: 0,
          trimUs: 15000,
        },
      ],
    }))
    const { server, user } = renderGroups({
      'POST /groups/g1/calibrate': scheduled,
      'POST /groups/g1/calibrations/c1': applied,
    }, '/groups/g1')
    await user.click(await screen.findByRole('button', { name: 'Calibrate' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Calibrate Upstairs?' })
    expect((within(confirm).getByRole('radio', { name: /This device's microphone/ }) as HTMLInputElement).checked).toBe(true)
    await user.click(within(confirm).getByRole('button', { name: 'Start listening' }))
    await waitFor(() => expect(confirm.isConnected).toBe(false), { timeout: 5000 })
    expect(await bodyOf(server, 'POST')).toEqual({ mode: 'listener' })
    const apply = server.requests.find(r => r.url.endsWith('/calibrations/c1'))!
    expect(await apply.clone().json()).toEqual({
      members: [
        {
          deviceId: 'd3',
          code: 'missed',
          arrivalUs: 0,
          confidence: 0,
          spreadUs: 0,
          rounds: 0,
        },
        {
          deviceId: 'd4',
          code: 'missed',
          arrivalUs: 0,
          confidence: 0,
          spreadUs: 0,
          rounds: 0,
        },
      ],
    })
    expect(listener.stop).toHaveBeenCalled()
    expect(await screen.findByText('Listener')).toBeTruthy()
    expect(rows('Calibration')[1]![1]).toBe('Not heard: its probe was not found (missed)')
    listener.supported.mockReturnValue(false)
  })

  it('retries louder: the faint member\'s chirps 6 dB up each time, the boosts dropped by a fresh run', async () => {
    const faint = json({
      code: 'incomplete',
      mode: 'mics',
      residualUs: 0,
      applied: false,
      members: [
        {
          deviceId: 'd3',
          code: 'ok',
          offsetUs: 0,
          confidence: 0.9,
          trimUs: 0,
          spreadUs: 20,
          rounds: 4,
          micChannels: 4,
        },
        {
          deviceId: 'd4',
          code: 'weak',
          offsetUs: 0,
          confidence: 0.1,
          trimUs: 0,
          spreadUs: 0,
          rounds: 0,
          micChannels: 2,
        },
      ],
    })
    const calibrate = vi.fn(faint)
    const { server, user } = renderGroups({ 'POST /groups/g1/calibrate': calibrate }, '/groups/g1')
    await user.click(await screen.findByRole('button', { name: 'Calibrate' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Calibrate Upstairs?' })
    await user.click(within(confirm).getByRole('button', { name: 'Play the chirps' }))
    await waitFor(() => expect(confirm.isConnected).toBe(false))
    expect(screen.getByText(/Plays only these speakers' chirps louder.*Nursery\./)).toBeTruthy()
    await user.click(await screen.findByRole('button', { name: 'Retry louder' }))
    await waitFor(() => expect(calibrate).toHaveBeenCalledTimes(2))
    await user.click(await screen.findByRole('button', { name: 'Retry louder' }))
    await waitFor(() => expect(calibrate).toHaveBeenCalledTimes(3))
    await user.click(screen.getByRole('button', { name: 'Calibrate' }))
    const again = await screen.findByRole('alertdialog', { name: 'Calibrate Upstairs?' })
    await user.click(within(again).getByRole('button', { name: 'Play the chirps' }))
    await waitFor(() => expect(calibrate).toHaveBeenCalledTimes(4))
    const posts = server.requests.filter(r => r.method === 'POST' && r.url.endsWith('/calibrate'))
    const bodies = await Promise.all(posts.map(async r => (await r.clone().text()) || null))
    expect(bodies.map(b => b && JSON.parse(b))).toEqual([
      null,
      {
        boost: [
          { deviceId: 'd4', gainDb: 6 },
        ],
      },
      {
        boost: [
          { deviceId: 'd4', gainDb: 12 },
        ],
      },
      null,
    ])
  })

  it('says a group that is gone is gone', async () => {
    renderGroups({
      'GET /groups/g1': json({ error: 'not_found' }, 404),
    }, '/groups/g1')
    expect(await screen.findByText('This group is gone')).toBeTruthy()
  })
})
