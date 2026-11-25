// @vitest-environment jsdom
import type { UserEvent } from '@testing-library/user-event'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TRANSCRIPT_GC_MS, TURNS_PAGE } from '@/lib/conversations'
import { EVENT_DETAIL, EVENT_TURN, FAILED_LLM_DETAIL, FAILED_LLM_TURN, FAILED_TOOL_DETAIL, FAILED_TOOL_TURN, FIRST_PAGE, LAST_PAGE, WAKE_DETAIL, WAKE_TURN } from '@/test/conversations'
import { detail, devicePage, ID, OTHER } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { queryOf } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { DevicePage } from '../device-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.useRealTimers()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

const TURNS = `GET /devices/${ID}/turns`

// the first page, then the last one for its cursor
const pages: FakeRoute = request => Response.json(queryOf(request).before === FIRST_PAGE.next
  ? LAST_PAGE
  : FIRST_PAGE)

function renderDevice(overrides: Record<string, FakeRoute> = {}) {
  return renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail(),
    }, {
      [TURNS]: pages,
      [`GET /devices/${ID}/turns/${WAKE_TURN.turnId}`]: json(WAKE_DETAIL),
      [`GET /devices/${ID}/turns/${EVENT_TURN.turnId}`]: json(EVENT_DETAIL),
      [`GET /devices/${ID}/turns/${FAILED_TOOL_TURN.turnId}`]: json(FAILED_TOOL_DETAIL),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
}

async function openConversations(user: UserEvent) {
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
  await user.click(screen.getByRole('tab', { name: 'Conversations' }))
  return screen.getByRole('tabpanel', { name: 'Conversations' })
}

function bodyRows(panel: HTMLElement) {
  return within(within(panel).getByRole('table', { name: 'Kitchen conversations' })).getAllByRole('row').slice(1)
}

function cells(row: HTMLElement) {
  return within(row).getAllByRole('cell').map(cell => cell.textContent)
}

describe('a device\'s conversations', () => {
  it('reads nothing until its tab is opened, then the newest page', async () => {
    const user = setupUser()
    const { server } = renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    await screen.findByText('Simulator')
    expect(server.calls(TURNS)).toBe(0)

    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    expect(server.calls(TURNS)).toBe(1)
    const request = server.requests.find(r => new URL(r.url).pathname.endsWith('/turns'))!
    expect(queryOf(request)).toEqual({
      limit: String(TURNS_PAGE),
    })
  })

  it('lists each turn with its time, trigger, mode, words, tools and tokens, as text', async () => {
    const user = setupUser()
    renderDevice()
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    const [wake, event, failed] = bodyRows(panel)

    expect(cells(wake!)).toEqual([
      '12 Nov, 21:04:12',
      'Wake word',
      'fast',
      'turn off <b>the</b> kitchen light',
      'Done, the light is off.',
      'ha_control',
      '1,200 in · 34 out',
      'Open',
    ])
    expect(panel.querySelector('b')).toBeNull()

    // an event turn's words are the server's framing, not anybody's speech
    expect(cells(event!).slice(1, 4)).toEqual(['Event', 'fast', 'Server\'s ownTask backup finished: 3 files copied.'])

    const [, , , said, reply, tools] = cells(failed!)
    expect(said!.endsWith('…')).toBe(true)
    expect([...said!].length).toBe(121)
    expect(reply).toBe('—')
    expect(tools).toBe('ha_control not_founddevice_volume')
    expect(within(failed!).getByText('not_found').getAttribute('data-tone')).toBe('danger')
  })

  it('loads older turns with the next cursor until the last page, the focus on the first of them', async () => {
    const user = setupUser()
    const { server } = renderDevice()
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')

    await user.click(within(panel).getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(bodyRows(panel)).toHaveLength(5))
    const older = server.requests.filter(r => new URL(r.url).pathname.endsWith('/turns'))[1]!
    expect(queryOf(older)).toEqual({
      before: 'cursor-1',
      limit: String(TURNS_PAGE),
    })
    expect(within(panel).queryByRole('button', { name: 'Load older' })).toBeNull()
    await expectFocusOn(within(bodyRows(panel)[3]!).getByRole('button', { name: /^Open the turn of/ }))

    expect(cells(bodyRows(panel)[3]!)).toEqual([
      '11 Nov, 15:00:00',
      'Not recorded',
      'None',
      'hello',
      '—',
      'None',
      'None',
      'Open',
    ])
    expect(cells(bodyRows(panel)[4]!)[1]).toBe('Follow-up')
  })

  it('leaves the overview as it was: an editor open there keeps its draft', async () => {
    const user = setupUser()
    renderDevice()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    const output = screen.getByRole('region', { name: 'audio.output' })
    await user.click(within(output).getByRole('button', { name: 'Edit the configuration' }))
    const editor = within(output).getByLabelText('Configuration of audio.output (JSON)') as HTMLTextAreaElement
    await user.clear(editor)
    await user.type(editor, '{{"level": 25}')
    await user.click(screen.getByRole('tab', { name: 'Conversations' }))
    await user.click(screen.getByRole('tab', { name: 'Overview' }))
    expect(editor.isConnected).toBe(true)
    expect(editor.value).toBe('{"level": 25}')
  })

  it('says when there is no conversation yet', async () => {
    const user = setupUser()
    renderDevice({
      [TURNS]: json({ turns: [] }),
    })
    const panel = await openConversations(user)
    expect(await within(panel).findByText('No conversations yet')).toBeTruthy()
    expect(within(panel).queryByRole('button', { name: 'Load older' })).toBeNull()
  })

  it('says why the turns could not be listed, in codes, and tries again', async () => {
    const user = setupUser()
    let fail = true
    renderDevice({
      [TURNS]: request => fail
        ? Response.json({ error: 'forbidden' }, { status: 403 })
        : pages(request),
    })
    const panel = await openConversations(user)
    expect((await within(panel).findByRole('alert')).textContent).toContain('The server answered 403 forbidden.')
    fail = false
    await user.click(within(panel).getByRole('button', { name: 'Try again' }))
    expect(await within(panel).findByText('Done, the light is off.')).toBeTruthy()
  })

  it('opens a turn\'s messages as stored, tool calls and results as preformatted text, never as markup', async () => {
    const user = setupUser()
    renderDevice()
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    const open = within(bodyRows(panel)[0]!).getByRole('button', { name: /^Open the turn of/ })
    expect(open.getAttribute('aria-label')).toBe('Open the turn of 12 Nov, 21:04:12')
    await user.click(open)

    const drawer = await screen.findByRole('dialog', { name: 'Turn of 12 Nov, 21:04:12' })
    expect(within(drawer).getByText('Wake word · fast · 1,200 in · 34 out')).toBeTruthy()
    const messages = await within(drawer).findByRole('list', { name: 'Messages' })
    expect(within(drawer).getByText('Skills loaded').parentElement?.textContent).toBe('Skills loadedhome')
    const items = within(messages).getAllByRole('listitem')
    expect(items.map(item => item.firstElementChild?.textContent)).toEqual([
      'User',
      'Assistant',
      'Tool result',
      'Assistant',
    ])
    expect(within(items[0]!).getByText('turn off <b>the</b> kitchen light')).toBeTruthy()

    const call = within(items[1]!).getByRole('figure', { name: 'Tool call ha_control' })
    expect(JSON.parse(call.querySelector('pre')!.textContent!)).toEqual({ entity: 'light.kitchen', action: 'turn_off' })
    const result = within(items[2]!).getByRole('figure', { name: 'Result of ha_control' })
    expect(result.querySelector('pre')!.textContent).toBe('{"ok":true,"note":"<img src=x onerror=alert(1)>"}')
    expect(within(items[3]!).getByText('Done, the light is off.')).toBeTruthy()
    expect(drawer.querySelectorAll('b, img, script')).toHaveLength(0)

    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectFocusOn(open)
  })

  it('names a tool result\'s error code in its caption, as the list names it', async () => {
    const user = setupUser()
    renderDevice()
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    await user.click(within(bodyRows(panel)[2]!).getByRole('button', { name: /^Open the turn of/ }))
    const drawer = await screen.findByRole('dialog', { name: 'Turn of 12 Nov, 20:58:30' })
    const messages = await within(drawer).findByRole('list', { name: 'Messages' })
    const result = within(messages).getByRole('figure', { name: 'Result of ha_control not_found' })
    const code = within(result).getByText('not_found', { selector: 'figcaption *' })
    expect(code.dataset.tone).toBe('danger')
    expect(result.querySelector('pre')!.textContent).toBe('{"error":"not_found"}')
  })

  it('marks an event turn\'s words as the server\'s own', async () => {
    const user = setupUser()
    renderDevice()
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    await user.click(within(bodyRows(panel)[1]!).getByRole('button', { name: /^Open the turn of/ }))
    const drawer = await screen.findByRole('dialog', { name: 'Turn of 12 Nov, 21:01:00' })
    const items = within(await within(drawer).findByRole('list', { name: 'Messages' })).getAllByRole('listitem')
    expect(items.map(item => item.firstElementChild?.textContent)).toEqual(['Server\'s own', 'Assistant'])
  })

  it('says why a turn could not be shown, in codes', async () => {
    const user = setupUser()
    renderDevice({
      [`GET /devices/${ID}/turns/${WAKE_TURN.turnId}`]: json({ error: 'not_found' }, 404),
    })
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    await user.click(within(bodyRows(panel)[0]!).getByRole('button', { name: /^Open the turn of/ }))
    const drawer = await screen.findByRole('dialog', { name: 'Turn of 12 Nov, 21:04:12' })
    expect((await within(drawer).findByRole('alert')).textContent).toContain('The server answered 404 not_found.')
  })

  it('marks a failed turn and shows the real stage, provider and timeout in its drawer', async () => {
    const user = setupUser()
    renderDevice({
      [TURNS]: json({
        turns: [FAILED_LLM_TURN],
      }),
      [`GET /devices/${ID}/turns/${FAILED_LLM_TURN.turnId}`]: json(FAILED_LLM_DETAIL),
    })
    const panel = await openConversations(user)
    const row = (await within(panel).findByText('what is on television?')).closest('tr')!
    expect(within(row).getByText('Failed').parentElement?.textContent).toBe('Failedtimeout')

    await user.click(within(row).getByRole('button', { name: /^Open the turn of/ }))
    const drawer = await screen.findByRole('dialog')
    const failure = await within(drawer).findByRole('region', { name: 'Turn failed' })
    expect(within(failure).getByText('LLM gateway did not answer within 10 s')).toBeTruthy()
    expect(within(failure).getByText('LLM call')).toBeTruthy()
    expect(within(failure).getByText('gateway-main')).toBeTruthy()
    expect(within(failure).getByText('vendor/model-x')).toBeTruthy()
    expect(within(failure).getByText('llm · step 1 · timeout · waited 10.0 s of 10.0 s')).toBeTruthy()
  })

  it('reads the list again when this device\'s turn ends on the live feed, and only this device\'s', async () => {
    const user = setupUser()
    const { server } = renderDevice()
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    act(() => FakeSocket.last.open())
    act(() => FakeSocket.last.receive({
      type: 'turn_end',
      deviceId: OTHER,
      at: '2026-11-12T15:05:00Z',
      data: { code: 'ok' },
    }))
    await new Promise(resolve => setTimeout(resolve, 400))
    expect(server.calls(TURNS)).toBe(1)
    act(() => FakeSocket.last.receive({
      type: 'turn_end',
      deviceId: ID,
      at: '2026-11-12T15:05:00Z',
      data: { code: 'ok' },
    }))
    await waitFor(() => expect(server.calls(TURNS)).toBe(2))
  })

  it('resets only once confirmed, compacts on demand, and refreshes the stored history', async () => {
    const user = setupUser()
    const reset = `POST /devices/${ID}/conversation/reset`
    const compact = `POST /devices/${ID}/conversation/compact`
    const { server } = renderDevice({
      [reset]: json({ reset: true }),
      [compact]: json({ accepted: true }, 202),
    })
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    expect(server.calls(TURNS)).toBe(1)

    await user.click(within(panel).getByRole('button', { name: 'Reset conversation' }))
    expect(server.calls(reset)).toBe(0)
    const dialog = await screen.findByRole('alertdialog', { name: 'Reset this conversation?' })
    expect(dialog.textContent).toContain('long-term memories are not deleted')
    await user.click(within(dialog).getByRole('button', { name: 'Reset conversation' }))
    await waitFor(() => expect(server.calls(reset)).toBe(1))
    await waitFor(() => expect(server.calls(TURNS)).toBe(2))
    expect(server.requests.find(r => new URL(r.url).pathname.endsWith('/conversation/reset'))?.method).toBe('POST')

    await user.click(within(panel).getByRole('button', { name: 'Compact now' }))
    await waitFor(() => expect(server.calls(compact)).toBe(1))
  })

  it('words a compact refusal beside its button', async () => {
    const user = setupUser()
    const compact = `POST /devices/${ID}/conversation/compact`
    renderDevice({
      [compact]: json({ error: 'busy' }, 409),
    })
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    await user.click(within(panel).getByRole('button', { name: 'Compact now' }))
    expect((await within(panel).findByRole('alert')).textContent).toContain('The server answered 409 busy.')
  })

  it('keeps transcripts in memory only, out of the address and storage, gone soon after they leave the page', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    const { router, queryClient } = renderDevice()
    const panel = await openConversations(user)
    await within(panel).findByText('Done, the light is off.')
    await user.click(within(bodyRows(panel)[0]!).getByRole('button', { name: /^Open the turn of/ }))
    const drawer = await screen.findByRole('dialog', { name: 'Turn of 12 Nov, 21:04:12' })
    await within(drawer).findByRole('list', { name: 'Messages' })

    expect(router.state.location.href).toBe(`/devices/${ID}`)
    const stored = [
      ...Object.values(localStorage),
      ...Object.values(sessionStorage),
    ].join('')
    expect(stored).not.toContain('kitchen light')

    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await user.click(screen.getByRole('tab', { name: 'Overview' }))
    const cached = () => queryClient.getQueryCache().getAll().filter(q => ['listDeviceTurns', 'getDeviceTurn'].includes((q.queryKey[0] as { _id: string })._id))
    expect(cached()).toHaveLength(2)
    await act(() => vi.advanceTimersByTimeAsync(TRANSCRIPT_GC_MS + 1))
    expect(cached()).toHaveLength(0)
  })
})
