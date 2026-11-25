// @vitest-environment jsdom
import type { CompactionJson, MemoryJson, RunJson } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, noContent, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { BEDROOM, DEVICES, KITCHEN, queryOf } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { MemoryPage } from './memory-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
  sessionStorage.clear()
})

function memory(over: Partial<MemoryJson> & Pick<MemoryJson, 'id' | 'text'>): MemoryJson {
  return {
    scope: 'global',
    deviceId: null,
    kind: 'fact',
    pinned: false,
    hits: 0,
    lastHitAt: null,
    confidence: 1,
    evidence: [],
    source: 'manual',
    status: 'active',
    embedded: true,
    createdAt: '2026-11-10T08:15:00Z',
    updatedAt: '2026-11-10T08:15:00Z',
    ...over,
  }
}

const DOG = memory({
  id: 'm1',
  text: 'The dog is called Bars.',
  hits: 4,
  updatedAt: '2026-11-11T21:40:00Z',
})
const COFFEE = memory({
  id: 'm2',
  text: 'Likes coffee without sugar.',
  kind: 'preference',
  scope: 'device',
  deviceId: KITCHEN,
  pinned: true,
  source: 'learned',
})
const LAMP = memory({
  id: 'p1',
  text: '"The big lamp" is light.living_room.',
  kind: 'alias',
  status: 'pending',
  source: 'learned',
  createdAt: '2026-11-12T09:00:00Z',
})
const UPDATE = memory({
  id: 'p2',
  text: 'The dog is called Barsik.',
  status: 'pending',
  source: 'learned',
  replaces: 'm1',
  proposal: 'update',
})
const FORGET = memory({
  id: 'p3',
  text: 'Likes coffee without sugar.',
  kind: 'preference',
  scope: 'device',
  deviceId: KITCHEN,
  status: 'pending',
  source: 'learned',
  replaces: 'm2',
  proposal: 'delete',
})

// the memories the server holds, narrowed by the list's query as the server does
function store(initial: MemoryJson[]) {
  const state = { list: initial }
  const route: FakeRoute = (request) => {
    const q = queryOf(request)
    const rows = state.list.filter(m => (q.status ?? 'active') === m.status
      && (!q.scope || q.scope === m.scope)
      && (!q.deviceId || q.deviceId === m.deviceId)
      && (!q.kind || q.kind === m.kind))
    const offset = Number(q.offset ?? 0)
    return Response.json(rows.slice(offset, offset + Number(q.limit ?? 50)))
  }
  return { state, route }
}

const RUNS: RunJson[] = [
  {
    id: 'r1',
    compactionId: 'c1',
    applied: 2,
    dropped: 1,
    pending: 0,
    memoryIds: ['m1', 'm2'],
    createdAt: '2026-11-11T10:00:00Z',
  },
  {
    id: 'r2',
    compactionId: 'c2',
    applied: 0,
    dropped: 0,
    pending: 1,
    memoryIds: ['p1'],
    createdAt: '2026-11-12T09:00:00Z',
  },
]

const COMPACTIONS: CompactionJson[] = [
  {
    id: 'c2',
    uptoMessageId: 120,
    summary: 'Talked about the lamp in the living room.',
    openThreads: ['the lamp\'s name'],
    model: 'deepseek-v4',
    createdAt: '2026-11-12T08:59:00Z',
  },
]

function renderMemory(routes: Record<string, FakeRoute> = {}) {
  const memories = store([DOG, COFFEE, LAMP, UPDATE, FORGET])
  const app = renderApp({
    path: '/memory',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /devices': json(DEVICES),
      'GET /memories': memories.route,
      [`GET /devices/${KITCHEN}/learning`]: json(RUNS),
      [`GET /devices/${KITCHEN}/compactions`]: json(COMPACTIONS),
      ...routes,
    },
    pages: { '/memory': MemoryPage },
  })
  return {
    ...app,
    memories,
    user: setupUser(),
  }
}

function table(name: string) {
  return screen.getByRole('table', { name })
}

function rows(name: string, columns: number): string[][] {
  return within(table(name)).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, columns).map(cell => cell.textContent ?? ''))
}

function addButton(): HTMLElement {
  return screen.getAllByRole('button', { name: 'Add a memory' })[0]!
}

function messageOf(field: HTMLElement): string | null | undefined {
  const id = `${field.id}-error`
  expect(field.getAttribute('aria-describedby')?.split(' ')).toContain(id)
  return document.getElementById(id)?.textContent
}

async function ready() {
  await screen.findByText('The dog is called Bars.')
  await waitFor(() => expect(rows('Memories', 6)[0]?.[5]).not.toContain('--'))
}

describe('the memories', () => {
  it('lists the active ones with their kind, scope, uses and last change, the device ones under its name', async () => {
    renderMemory()
    await ready()
    expect(rows('Memories', 6)).toEqual([
      [
        'The dog is called Bars.',
        'Fact',
        'Global',
        '',
        '4',
        '12 Nov, 03:40',
      ],
      [
        'Likes coffee without sugar.',
        'Preference',
        'Kitchen',
        'Pinned',
        '0',
        '10 Nov, 14:15',
      ],
    ])
  })

  it('narrows them by scope and kind, as the server filters them', async () => {
    const { server, user } = renderMemory()
    await ready()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Scope' }), 'Kitchen')
    await waitFor(() => expect(rows('Memories', 1)).toEqual([
      ['Likes coffee without sugar.'],
    ]))
    expect(queryOf(server.requests.filter(r => r.url.includes('/memories?')).at(-1)!)).toMatchObject({
      status: 'active',
      scope: 'device',
      deviceId: KITCHEN,
    })
    await user.selectOptions(screen.getByRole('combobox', { name: 'Kind' }), 'Fact')
    expect(await within(table('Memories')).findByText('No memories match')).toBeTruthy()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Scope' }), 'Global')
    await waitFor(() => expect(rows('Memories', 1)).toEqual([
      ['The dog is called Bars.'],
    ]))
  })

  it('adds one with its text, kind and scope, then lists it and gives the focus back', async () => {
    const { server, memories, queryClient, user } = renderMemory({
      'POST /memories': async (request) => {
        const body = await request.clone().json() as { text: string, kind: string, deviceId: string }
        const created = memory({
          id: 'm9',
          text: body.text,
          kind: body.kind,
          scope: 'device',
          deviceId: body.deviceId,
        })
        memories.state.list = [...memories.state.list, created]
        return Response.json(created, { status: 201 })
      },
    })
    await ready()
    await user.click(addButton())
    const dialog = await screen.findByRole('dialog', { name: 'Add a memory' })
    const text = within(dialog).getByRole('textbox', { name: 'Text (required)' })
    await expectFocusOn(text)
    await user.type(text, 'Wakes up at seven.')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Kind (required)' }), 'Preference')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Scope (required)' }), 'Kitchen')
    await user.click(within(dialog).getByRole('button', { name: 'Add the memory' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    const post = server.requests.find(r => r.method === 'POST')!
    expect(post.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await post.clone().json()).toEqual({
      scope: 'device',
      deviceId: KITCHEN,
      kind: 'preference',
      text: 'Wakes up at seven.',
    })
    expect(await screen.findByText('Wakes up at seven.')).toBeTruthy()
    await expectFocusOn(addButton())
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0)
    expect(JSON.stringify({
      ...localStorage,
      ...sessionStorage,
    })).not.toContain('seven')
  })

  it('puts the server\'s refusal of the text on the text, and holds an empty one before sending', async () => {
    const { server, user } = renderMemory({
      'POST /memories': json({ error: 'looks_like_secret', path: 'text' }, 422),
    })
    await ready()
    await user.click(addButton())
    const dialog = await screen.findByRole('dialog', { name: 'Add a memory' })
    const text = within(dialog).getByRole('textbox', { name: 'Text (required)' })
    await user.click(within(dialog).getByRole('button', { name: 'Add the memory' }))
    expect(messageOf(text)).toBe('Required.')
    expect(server.calls('POST /memories')).toBe(0)
    await user.type(text, 'The wifi password is hunter2.')
    await user.click(within(dialog).getByRole('button', { name: 'Add the memory' }))
    await waitFor(() => expect(text.getAttribute('aria-invalid')).toBe('true'))
    expect(messageOf(text)).toBe('The server refused this value (looks_like_secret).')
    await expectFocusOn(text)
  })

  it('edits one, sending only what changed, and gives the focus back to its button', async () => {
    const { server, user } = renderMemory({
      'PATCH /memories/m1': json({ ...DOG, text: 'The dog is called Sharik.', pinned: true }),
    })
    await ready()
    const edit = screen.getByRole('button', { name: 'Edit: The dog is called Bars.' })
    await user.click(edit)
    const dialog = await screen.findByRole('dialog', { name: 'Edit the memory' })
    const text = within(dialog).getByRole('textbox', { name: 'Text (required)' })
    expect((text as HTMLTextAreaElement).value).toBe('The dog is called Bars.')
    await user.clear(text)
    await user.type(text, 'The dog is called Sharik.')
    await user.click(within(dialog).getByRole('checkbox', { name: 'Pinned' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save the memory' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await server.requests.find(r => r.method === 'PATCH')!.clone().json()).toEqual({
      text: 'The dog is called Sharik.',
      pinned: true,
    })
    await waitFor(() => expect(document.activeElement).toBe(edit))
  })

  it('moves one to another scope with its device', async () => {
    const { server, user } = renderMemory({
      'PATCH /memories/m2': json({ ...COFFEE, scope: 'global', deviceId: null }),
    })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Edit: Likes coffee without sugar.' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit the memory' })
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Scope (required)' }), 'Global (every speaker)')
    await user.click(within(dialog).getByRole('button', { name: 'Save the memory' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(await server.requests.find(r => r.method === 'PATCH')!.clone().json()).toEqual({ scope: 'global' })
  })

  it('gives the focus to the page\'s action when an edit moves the memory out of the filter, its row gone', async () => {
    const { memories, user } = renderMemory({
      'PATCH /memories/m2': () => {
        memories.state.list = memories.state.list.map(m => m.id === 'm2'
          ? { ...m, scope: 'global', deviceId: null }
          : m)
        return Response.json({ ...COFFEE, scope: 'global', deviceId: null })
      },
    })
    await ready()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Scope' }), 'Kitchen')
    await waitFor(() => expect(rows('Memories', 1)).toEqual([
      ['Likes coffee without sugar.'],
    ]))
    await user.click(screen.getByRole('button', { name: 'Edit: Likes coffee without sugar.' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit the memory' })
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Scope (required)' }), 'Global (every speaker)')
    await user.click(within(dialog).getByRole('button', { name: 'Save the memory' }))
    await waitFor(() => expect(within(table('Memories')).queryByText('Likes coffee without sugar.')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(addButton()))
  })

  it('deletes one only once confirmed, then gives the focus to the page\'s action', async () => {
    const { server, memories, user } = renderMemory({
      'DELETE /memories/m1': () => {
        memories.state.list = memories.state.list.filter(m => m.id !== 'm1')
        return new Response(null, { status: 204 })
      },
    })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Delete: The dog is called Bars.' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete this memory?' })
    expect(within(dialog).getByText('The dog is called Bars.')).toBeTruthy()
    await user.click(within(dialog).getByRole('button', { name: 'Delete the memory' }))
    await waitFor(() => expect(within(table('Memories')).queryByText('The dog is called Bars.')).toBeNull())
    expect(server.calls('DELETE /memories/m1')).toBe(1)
    await waitFor(() => expect(document.activeElement).toBe(addButton()))
  })

  it('offers more once a page is full', async () => {
    const many = Array.from({ length: 51 }, (_, i) => memory({ id: `n${i}`, text: `Note ${String(i).padStart(2, '0')}` }))
    const { server, user } = renderMemory({
      'GET /memories': store(many).route,
    })
    await screen.findByText('Note 00')
    expect(within(table('Memories')).queryByText('Note 50')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Show more memories' }))
    expect(await screen.findByText('Note 50')).toBeTruthy()
    expect(queryOf(server.requests.filter(r => r.url.includes('status=active')).at(-1)!).offset).toBe('50')
    expect(screen.queryByRole('button', { name: 'Show more memories' })).toBeNull()
  })

  it('follows the agent topic: a changed memory reads the lists again', async () => {
    const { server } = renderMemory()
    await ready()
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toEqual([
      {
        subscribe: ['agent', 'presence'],
      },
    ]))
    const before = server.calls('GET /memories')
    act(() => FakeSocket.last.receive({
      type: 'memory_changed',
      data: { id: 'm1', op: 'updated' },
    }))
    await waitFor(() => expect(server.calls('GET /memories')).toBeGreaterThan(before))
  })
})

describe('the suggestions', () => {
  it('list what learning proposes: new memories, updates and deletions, each with its own actions', async () => {
    renderMemory()
    await ready()
    // an update shows the active text it would overwrite
    await waitFor(() => expect(rows('Suggestions', 1)[1]?.[0]).toBe('The dog is called Barsik.Replaces: The dog is called Bars.'))
    expect(rows('Suggestions', 4)).toEqual([
      [
        '"The big lamp" is light.living_room.',
        'New',
        'Alias',
        'Global',
      ],
      [
        'The dog is called Barsik.Replaces: The dog is called Bars.',
        'Update',
        'Fact',
        'Global',
      ],
      [
        'Likes coffee without sugar.',
        'Delete',
        'Preference',
        'Kitchen',
      ],
    ])
    const inbox = table('Suggestions')
    expect(within(inbox).getByRole('button', { name: 'Edit and approve: "The big lamp" is light.living_room.' })).toBeTruthy()
    // a proposal is approved or rejected as made: the server edits no proposal
    expect(within(inbox).queryByRole('button', { name: 'Edit and approve: The dog is called Barsik.' })).toBeNull()
    expect(within(inbox).queryByRole('button', { name: 'Edit and approve: Likes coffee without sugar.' })).toBeNull()
  })

  it('count in the nav', async () => {
    renderMemory()
    await ready()
    const link = within(screen.getByRole('navigation', { name: 'Main' })).getByRole('link', { name: /Memory/ })
    await waitFor(() => expect(link.textContent).toBe('Memory3, 3 suggestions to review'))
  })

  it('approve a new memory and reject an update, reading the lists again', async () => {
    const { server, memories, user } = renderMemory({
      'POST /memories/p1/approve': () => {
        memories.state.list = memories.state.list.map(m => m.id === 'p1'
          ? { ...m, status: 'active' }
          : m)
        return Response.json({ ...LAMP, status: 'active' })
      },
      'POST /memories/p2/reject': () => {
        memories.state.list = memories.state.list.filter(m => m.id !== 'p2')
        return new Response(null, { status: 204 })
      },
    })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Approve: "The big lamp" is light.living_room.' }))
    await waitFor(() => expect(within(table('Memories')).getByText('"The big lamp" is light.living_room.')).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Reject: The dog is called Barsik.' }))
    await waitFor(() => expect(within(table('Suggestions')).queryByText('The dog is called Barsik.')).toBeNull())
    expect(server.calls('POST /memories/p1/approve')).toBe(1)
    expect(server.calls('POST /memories/p2/reject')).toBe(1)
    expect(server.requests.find(r => r.url.endsWith('/approve'))!.headers.get('X-Cadence-CSRF')).toBe('1')
  })

  it('word a refused approval beside the table, keeping the focus on its button', async () => {
    const { user } = renderMemory({
      'POST /memories/p2/approve': json({ error: 'conflict' }, 409),
    })
    await ready()
    const approve = screen.getByRole('button', { name: 'Approve: The dog is called Barsik.' })
    await user.click(approve)
    const inbox = screen.getByRole('region', { name: 'Suggestions' })
    expect((await within(inbox).findByRole('alert')).textContent).toBe('The suggestion was not approved. The server answered 409 conflict.')
    expect(document.activeElement).toBe(approve)
  })

  it('ask before approving a deletion', async () => {
    const { server, user } = renderMemory({
      'POST /memories/p3/approve': noContent(),
    })
    await ready()
    await user.click(screen.getByRole('button', { name: 'Approve: Likes coffee without sugar.' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete the memory it names?' })
    // the confirmation shows the memory it would delete
    expect(within(dialog).getByText('Likes coffee without sugar.')).toBeTruthy()
    expect(server.calls('POST /memories/p3/approve')).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: 'Approve the deletion' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(server.calls('POST /memories/p3/approve')).toBe(1)
    // its row goes: the focus waits on the section's heading
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 2, name: 'Suggestions' })))
  })

  it('give the focus back to the edit button when an edit is cancelled', async () => {
    const { server, user } = renderMemory()
    await ready()
    const trigger = screen.getByRole('button', { name: 'Edit and approve: "The big lamp" is light.living_room.' })
    await user.click(trigger)
    const dialog = await screen.findByRole('dialog', { name: 'Edit and approve' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    await expectFocusOn(trigger)
    expect(server.requests.some(r => r.method !== 'GET')).toBe(false)
  })

  it('edit a new memory, then approve it: the patch first, the approval after', async () => {
    const { server, user } = renderMemory({
      'PATCH /memories/p1': json({ ...LAMP, text: '"The big lamp" is light.living_room_main.' }),
      'POST /memories/p1/approve': json({ ...LAMP, status: 'active' }),
    })
    await ready()
    const trigger = screen.getByRole('button', { name: 'Edit and approve: "The big lamp" is light.living_room.' })
    await user.click(trigger)
    const dialog = await screen.findByRole('dialog', { name: 'Edit and approve' })
    const text = within(dialog).getByRole('textbox', { name: 'Text (required)' })
    await user.clear(text)
    await user.type(text, '"The big lamp" is light.living_room_main.')
    await user.click(within(dialog).getByRole('button', { name: 'Approve with these changes' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    const sent = server.requests.filter(r => r.method !== 'GET').map(r => `${r.method} ${new URL(r.url).pathname}`)
    expect(sent).toEqual(['PATCH /api/v1/memories/p1', 'POST /api/v1/memories/p1/approve'])
    expect(await server.requests.find(r => r.method === 'PATCH')!.clone().json()).toEqual({ text: '"The big lamp" is light.living_room_main.' })
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 2, name: 'Suggestions' })))
  })

  it('say when there is nothing to review', async () => {
    renderMemory({
      'GET /memories': store([DOG]).route,
    })
    await screen.findByText('The dog is called Bars.')
    expect(within(table('Suggestions')).getByText('No suggestions')).toBeTruthy()
  })
})

describe('the learning log', () => {
  it('shows a speaker\'s runs and compactions, newest first, and asks for more', async () => {
    const { server } = renderMemory()
    await ready()
    expect((screen.getByRole('combobox', { name: 'Speaker' }) as HTMLSelectElement).value).toBe(KITCHEN)
    await waitFor(() => expect(rows('Learning runs', 5)).toEqual([
      ['12 Nov, 15:00', '0', '1', '0', '1'],
      ['11 Nov, 16:00', '2', '0', '1', '2'],
    ]))
    expect(rows('Compactions', 3)).toEqual([
      ['12 Nov, 14:59', 'deepseek-v4', 'Talked about the lamp in the living room.'],
    ])
    expect(screen.queryByRole('button', { name: 'Show more runs' })).toBeNull()
    expect(queryOf(server.requests.find(r => r.url.includes('/learning'))!).limit).toBe('20')
  })

  it('reads the speaker chosen', async () => {
    const { server, user } = renderMemory({
      [`GET /devices/${BEDROOM}/learning`]: json([]),
      [`GET /devices/${BEDROOM}/compactions`]: json([]),
    })
    await ready()
    await screen.findByRole('table', { name: 'Learning runs' })
    await user.selectOptions(screen.getByRole('combobox', { name: 'Speaker' }), 'Bedroom')
    await waitFor(() => expect(server.calls(`GET /devices/${BEDROOM}/learning`)).toBe(1))
    expect(server.calls(`GET /devices/${BEDROOM}/compactions`)).toBe(1)
    expect(await screen.findByText('Nothing learned yet')).toBeTruthy()
  })

  it('reads more of a full page, up to the server\'s limit', async () => {
    const full = Array.from({ length: 20 }, (_, i) => ({ ...RUNS[0]!, id: `r${i}` }))
    const { server, user } = renderMemory({
      [`GET /devices/${KITCHEN}/learning`]: json(full),
    })
    await ready()
    await user.click(await screen.findByRole('button', { name: 'Show more runs' }))
    await waitFor(() => expect(queryOf(server.requests.filter(r => r.url.includes('/learning')).at(-1)!).limit).toBe('40'))
  })
})

it('has no accessibility violations axe can see without layout', async () => {
  renderMemory()
  await ready()
  await screen.findByRole('table', { name: 'Learning runs' })
  // jsdom has no canvas; axe only probes for one
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const results = await axe.run(document.body, {
    rules: {
      'color-contrast': { enabled: false },
    },
  })
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})
