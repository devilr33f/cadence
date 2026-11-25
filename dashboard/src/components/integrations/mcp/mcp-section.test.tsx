// @vitest-environment jsdom
import type { ToolView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { ECHO, HOSTILE, integrationRoutes, NOTE_ADD } from '@/test/integrations'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { IntegrationsPage } from '../integrations-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function renderMcp(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/integrations',
    routes: integrationRoutes(routes),
    pages: { '/integrations': IntegrationsPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function tools() {
  return screen.getByRole('table', { name: 'Tools of notes' })
}

function rows(): string[][] {
  return within(tools()).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 5).map(cell => cell.textContent ?? ''))
}

// the tools the server lists, changed by the test's patch and acknowledge routes
function toolStore(initial: ToolView[]) {
  const state = { list: initial }
  return {
    state,
    route: (() => Response.json(state.list)) satisfies FakeRoute,
  }
}

describe('the mcp servers', () => {
  it('show each server\'s state and its tools, a description as text whatever it holds', async () => {
    renderMcp()
    const mcp = await screen.findByRole('region', { name: 'MCP servers' })
    expect(await within(mcp).findByRole('heading', { level: 3, name: 'notes' })).toBeTruthy()
    expect(within(mcp).getByText('Connected')).toBeTruthy()
    expect(within(mcp).getByText('mcp.example:8443')).toBeTruthy()
    expect(within(mcp).getByText('2 tools: 1 offered, 1 changed, 1 not offered')).toBeTruthy()
    await within(mcp).findByRole('table', { name: 'Tools of notes' })
    expect(rows()).toEqual([
      [
        'mcp_notes_echoecho',
        HOSTILE,
        'Write · untrusted',
        'Off',
        'Needs acknowledgement',
      ],
      [
        'mcp_notes_note_addnote_add',
        '[MCP notes] Add a note',
        'Write · untrusted',
        'Off',
        'Available',
      ],
    ])
    expect(within(tools()).getByText(HOSTILE)).toBeTruthy()
    expect(document.querySelector('main script, main b')).toBeNull()
  })

  it('ask a server for its tools again, then read the list again', async () => {
    const { server, user } = renderMcp({
      'POST /mcp/servers/notes/refresh': json({ accepted: true }, 202),
    })
    await screen.findByRole('table', { name: 'Tools of notes' })
    const before = server.calls('GET /mcp/servers')
    await user.click(screen.getByRole('button', { name: 'Refresh notes' }))
    await waitFor(() => expect(server.calls('POST /mcp/servers/notes/refresh')).toBe(1))
    expect(server.requests.find(r => r.method === 'POST')!.headers.get('X-Cadence-CSRF')).toBe('1')
    await waitFor(() => expect(server.calls('GET /mcp/servers')).toBeGreaterThan(before))
  })

  it('word a refused refresh beside its button, which keeps the focus', async () => {
    const { user } = renderMcp({
      'POST /mcp/servers/notes/refresh': json({ error: 'not_found' }, 404),
    })
    await screen.findByRole('table', { name: 'Tools of notes' })
    const refresh = screen.getByRole('button', { name: 'Refresh notes' })
    await user.click(refresh)
    const server = screen.getByRole('region', { name: 'notes' })
    expect((await within(server).findByRole('alert')).textContent).toBe('notes was not refreshed. The server answered 404 not_found.')
    expect(document.activeElement).toBe(refresh)
  })

  it('follow the agent topic: a server\'s changed tools read the lists again', async () => {
    const { server } = renderMcp()
    await screen.findByRole('table', { name: 'Tools of notes' })
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toEqual([
      {
        subscribe: ['agent', 'presence', 'subscriptions'],
      },
    ]))
    const before = server.calls('GET /mcp/servers/notes/tools')
    act(() => FakeSocket.last.receive({
      type: 'mcp_tools_changed',
      data: {
        instance: 'notes',
        total: 2,
        offered: 2,
        changed: 0,
        skipped: 0,
      },
    }))
    await waitFor(() => expect(server.calls('GET /mcp/servers/notes/tools')).toBeGreaterThan(before))
  })

  it('teach how to add one when there is none', async () => {
    renderMcp({
      'GET /mcp/servers': json([]),
    })
    expect(await screen.findByText('No MCP servers')).toBeTruthy()
    const mcp = screen.getByRole('region', { name: 'MCP servers' })
    expect(within(mcp).getByRole('link', { name: 'Open the mcp setting' }).getAttribute('href')).toBe('/settings#setting-mcp')
  })
})

describe('a tool\'s access', () => {
  it('changes only what the owner changed, and gives the focus back to its button', async () => {
    const store = toolStore([ECHO, NOTE_ADD])
    const { server, user } = renderMcp({
      'GET /mcp/servers/notes/tools': store.route,
      'PATCH /mcp/servers/notes/tools/note_add': async (request) => {
        const patch = await request.clone().json() as Partial<ToolView>
        const updated = {
          ...NOTE_ADD,
          ...patch,
        }
        store.state.list = [ECHO, updated]
        return Response.json(updated)
      },
    })
    const edit = await screen.findByRole('button', { name: 'Edit access: mcp_notes_note_add' })
    await user.click(edit)
    const dialog = await screen.findByRole('dialog', { name: 'Access of mcp_notes_note_add' })
    expect((within(dialog).getByRole('radio', { name: /^Write/ }) as HTMLInputElement).checked).toBe(true)
    await user.click(within(dialog).getByRole('radio', { name: /^Read/ }))
    await user.click(within(dialog).getByRole('checkbox', { name: 'Offer it to every speaker without a tools list' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save the access' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    const patch = server.requests.find(r => r.method === 'PATCH')!
    expect(patch.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await patch.clone().json()).toEqual({ effects: 'read', enabledDefault: true })
    await waitFor(() => expect(rows()[1]?.slice(2, 4)).toEqual(['Read · untrusted', 'On']))
    await waitFor(() => expect(document.activeElement).toBe(edit))
  })

  it('sends nothing when nothing changed', async () => {
    const { server, user } = renderMcp()
    await screen.findByRole('button', { name: 'Edit access: mcp_notes_note_add' })
    await user.click(screen.getByRole('button', { name: 'Edit access: mcp_notes_note_add' }))
    const dialog = await screen.findByRole('dialog', { name: 'Access of mcp_notes_note_add' })
    await user.click(within(dialog).getByRole('button', { name: 'Save the access' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    expect(server.requests.some(r => r.method === 'PATCH')).toBe(false)
  })
})

describe('acknowledging a changed tool', () => {
  it('shows what changed, and acknowledges the hash the page showed', async () => {
    const store = toolStore([ECHO, NOTE_ADD])
    const { server, user } = renderMcp({
      'GET /mcp/servers/notes/tools': store.route,
      'POST /mcp/servers/notes/acknowledge': () => {
        store.state.list = [
          {
            ...ECHO,
            changed: false,
            skip: undefined,
            ackedHash: undefined,
          },
          NOTE_ADD,
        ]
        return Response.json(store.state.list)
      },
    })
    await screen.findByRole('button', { name: 'Edit access: mcp_notes_note_add' })
    expect(within(tools()).queryByRole('button', { name: 'Acknowledge: mcp_notes_note_add' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Acknowledge: mcp_notes_echo' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Acknowledge mcp_notes_echo?' })
    expect(within(dialog).getByText(HOSTILE)).toBeTruthy()
    expect(server.calls('POST /mcp/servers/notes/acknowledge')).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: 'Acknowledge the tool' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    const post = server.requests.find(r => r.method === 'POST')!
    expect(post.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await post.clone().json()).toEqual({
      hashes: { echo: 'h2' },
    })
    await waitFor(() => expect(rows()[0]?.[4]).toBe('Available'))
    // its button went with the change: the focus waits on the server's heading
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 3, name: 'notes' })))
  })

  it('never sends a hash the owner did not see: a change read while the dialog is open blocks it until reopened', async () => {
    const store = toolStore([ECHO, NOTE_ADD])
    const AGAIN = '[MCP notes] Echo, now also deletes every note'
    const { server, queryClient, user } = renderMcp({
      'GET /mcp/servers/notes/tools': store.route,
      'POST /mcp/servers/notes/acknowledge': () => Response.json(store.state.list),
    })
    await screen.findByRole('button', { name: 'Edit access: mcp_notes_note_add' })
    await user.click(screen.getByRole('button', { name: 'Acknowledge: mcp_notes_echo' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Acknowledge mcp_notes_echo?' })

    // the tool changes again on the server, and a refetch (a focus, another server's event) reads it
    store.state.list = [
      { ...ECHO, hash: 'h3', description: AGAIN },
      NOTE_ADD,
    ]
    await act(() => queryClient.invalidateQueries({
      queryKey: [
        { _id: 'listMcpTools' },
      ],
    }))
    // the page behind the modal (hidden from the accessibility tree while it is open) already shows the new one
    await waitFor(() => expect(within(screen.getByRole('table', { name: 'Tools of notes', hidden: true })).getByText(AGAIN)).toBeTruthy())
    expect(within(dialog).getByText(HOSTILE)).toBeTruthy()
    expect(within(dialog).queryByText(AGAIN)).toBeNull()
    expect(within(dialog).getByRole('status').textContent).toContain('Changed since you opened this')
    await user.click(within(dialog).getByRole('button', { name: 'Acknowledge the tool' }))
    expect(server.calls('POST /mcp/servers/notes/acknowledge')).toBe(0)

    // opened again, it shows the new description and sends its hash
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    await user.click(screen.getByRole('button', { name: 'Acknowledge: mcp_notes_echo' }))
    const again = await screen.findByRole('alertdialog', { name: 'Acknowledge mcp_notes_echo?' })
    expect(within(again).getByText(AGAIN)).toBeTruthy()
    await user.click(within(again).getByRole('button', { name: 'Acknowledge the tool' }))
    await waitFor(() => expect(server.calls('POST /mcp/servers/notes/acknowledge')).toBe(1))
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({
      hashes: { echo: 'h3' },
    })
  })

  it('words a tool that changed again since the page showed it, and reads the tools again', async () => {
    const { server, user } = renderMcp({
      'POST /mcp/servers/notes/acknowledge': json({ error: 'hash_mismatch' }, 409),
    })
    await screen.findByRole('button', { name: 'Edit access: mcp_notes_note_add' })
    const before = server.calls('GET /mcp/servers/notes/tools')
    await user.click(screen.getByRole('button', { name: 'Acknowledge: mcp_notes_echo' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Acknowledge mcp_notes_echo?' })
    await user.click(within(dialog).getByRole('button', { name: 'Acknowledge the tool' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 409 hash_mismatch.')
    await waitFor(() => expect(server.calls('GET /mcp/servers/notes/tools')).toBeGreaterThan(before))
  })
})
