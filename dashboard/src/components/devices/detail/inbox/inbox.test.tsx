// @vitest-environment jsdom
import type { UserEvent } from '@testing-library/user-event'
import type { InboxItem } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { detail, devicePage, ID } from '@/test/device-detail'
import { json } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { queryOf } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { APPROVAL, CLARIFY, HOSTILE_QUESTION, HOSTILE_TITLE, INBOX } from '@/test/subscriptions'
import { renderApp } from '@/test/test-app'
import { enter, setupUser } from '@/test/typing'
import { DevicePage } from '../device-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

const INBOX_ROUTE = 'GET /subscriptions/inbox'
const REPLY = 'POST /subscriptions/reply'

// the inbox the server holds; a reply takes its question out of it, as the server marks it answered
function inboxStore(initial: InboxItem[]) {
  const state = { items: initial }
  return {
    state,
    route: (() => Response.json({ items: state.items })) satisfies FakeRoute,
    answer: (token: string) => {
      state.items = state.items.map(item => item.question?.token === token
        ? { ...item, open: false }
        : item)
    },
  }
}

function renderInbox(overrides: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: `/devices/${ID}`,
    routes: devicePage({
      detail: detail(),
    }, {
      [INBOX_ROUTE]: json({ items: INBOX }),
      ...overrides,
    }),
    pages: { '/devices/$id': DevicePage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

async function openInbox(user: UserEvent) {
  await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
  await user.click(screen.getByRole('tab', { name: 'Inbox' }))
  const panel = screen.getByRole('tabpanel', { name: 'Inbox' })
  await within(panel).findByRole('heading', { name: 'Open questions' })
  return panel
}

function question(panel: HTMLElement, text: string) {
  return within(panel).getByRole('listitem', { name: text })
}

function bodies(server: ReturnType<typeof renderApp>['server'], key: string) {
  return Promise.all(server.requests
    .filter(r => `${r.method} ${new URL(r.url).pathname.replace(/^\/api\/v1/, '')}` === key)
    .map(r => r.clone().json() as Promise<unknown>))
}

describe('a device\'s inbox', () => {
  it('reads nothing until its tab is opened, then the device\'s inbox', async () => {
    const { server, user } = renderInbox()
    await screen.findByRole('heading', { level: 1, name: 'Kitchen' })
    await screen.findByText('Simulator')
    expect(server.calls(INBOX_ROUTE)).toBe(0)
    await openInbox(user)
    expect(server.calls(INBOX_ROUTE)).toBe(1)
    const request = server.requests.find(r => new URL(r.url).pathname.endsWith('/subscriptions/inbox'))!
    expect(queryOf(request)).toEqual({ device: ID })
  })

  it('shows the open questions, then the updates by topic with their states, outside text as text', async () => {
    const { user } = renderInbox()
    const panel = await openInbox(user)
    expect(within(panel).getAllByText(HOSTILE_TITLE).length).toBeGreaterThan(0)
    expect(within(panel).getAllByText(HOSTILE_QUESTION).length).toBeGreaterThan(0)
    expect(within(panel).getByText('Finished <i>the</i> work')).toBeTruthy()
    expect(document.querySelector('main script, main b, main img, main i')).toBeNull()

    const updates = within(panel).getByRole('region', { name: 'Updates' })
    const groups = within(updates).getAllByRole('region')
    // the topic with the newest update first
    expect(groups.map(group => within(group).getByRole('heading').textContent)).toEqual(['Dashboard pages', HOSTILE_TITLE])
    const states = within(groups[1]!).getAllByRole('listitem').map(item => item.textContent)
    expect(states[0]).toContain('Unread')
    expect(states[1]).toContain('Held while busy')
    expect(within(groups[0]!).getAllByRole('listitem').map(item => item.textContent).join(' ')).toContain('Read')
    // nothing of the outside text in the url or the browser's storage
    for (const stored of [
      window.location.href,
      JSON.stringify({ ...localStorage }),
      JSON.stringify({ ...sessionStorage }),
    ]) {
      expect(stored).not.toContain('login')
      expect(stored).not.toContain('migration')
    }
  })

  it('answers a clarify question with an option, by its token, and gives the focus to the questions', async () => {
    const store = inboxStore(INBOX)
    const { server, user } = renderInbox({
      [INBOX_ROUTE]: store.route,
      [REPLY]: () => {
        store.answer(CLARIFY.question!.token)
        return Response.json({
          item: { ...CLARIFY, open: false },
        })
      },
    })
    const panel = await openInbox(user)
    const card = question(panel, HOSTILE_QUESTION)
    await user.click(within(card).getByRole('radio', { name: 'Not yet' }))
    await user.click(within(card).getByRole('button', { name: 'Send the answer' }))
    await expectFocusOn(within(panel).getByRole('heading', { name: 'Open questions' }))
    expect(await bodies(server, REPLY)).toEqual([
      {
        device: ID,
        token: 'tok-clarify',
        answers: ['b'],
      },
    ])
    await waitFor(() => expect(within(panel).queryByRole('listitem', { name: HOSTILE_QUESTION })).toBeNull())
  })

  it('answers in the owner\'s own words where the question takes them, never with an option too', async () => {
    const { server, user } = renderInbox({
      [REPLY]: json({ item: CLARIFY }),
    })
    const panel = await openInbox(user)
    const card = question(panel, HOSTILE_QUESTION)
    const send = within(card).getByRole('button', { name: 'Send the answer' })
    await user.click(send)
    expect(within(card).getByRole('alert').textContent).toBe('Choose an option or write an answer.')

    await user.click(within(card).getByRole('radio', { name: 'Yes, run it' }))
    const field = within(card).getByRole('textbox', { name: 'Or your own answer' })
    await enter(user, field, 'after lunch')
    await user.click(send)
    expect(within(card).getByRole('alert').textContent).toBe('Choose an option or write an answer, not both.')
    expect(document.activeElement).toBe(field)
    expect(server.calls(REPLY)).toBe(0)

    await user.click(within(card).getByRole('button', { name: 'Clear the choice' }))
    await user.click(send)
    await waitFor(() => expect(server.calls(REPLY)).toBe(1))
    expect(await bodies(server, REPLY)).toEqual([
      { device: ID, token: 'tok-clarify', message: 'after lunch' },
    ])
  })

  it('denies an approval unless another decision is chosen', async () => {
    const { server, user } = renderInbox({
      [REPLY]: json({ item: APPROVAL }),
    })
    const panel = await openInbox(user)
    const card = question(panel, 'Write to src/lib/busy.ts')
    expect((within(card).getByRole('radio', { name: /^Deny/ }) as HTMLInputElement).checked).toBe(true)
    await user.click(within(card).getByRole('button', { name: 'Send the denial' }))
    await waitFor(() => expect(server.calls(REPLY)).toBe(1))

    await user.click(within(card).getByRole('radio', { name: /^Approve for the session/ }))
    await user.click(within(card).getByRole('button', { name: 'Approve for the session' }))
    await waitFor(() => expect(server.calls(REPLY)).toBe(2))
    expect(await bodies(server, REPLY)).toEqual([
      { device: ID, token: 'tok-approval', decision: 'deny' },
      { device: ID, token: 'tok-approval', decision: 'session' },
    ])
  })

  it('words a refusal by its code, and keeps the form for another try', async () => {
    const { user } = renderInbox({
      [REPLY]: json({ error: 'not_found' }, 404),
    })
    const panel = await openInbox(user)
    const card = question(panel, 'Write to src/lib/busy.ts')
    const send = within(card).getByRole('button', { name: 'Send the denial' })
    await user.click(send)
    expect((await within(card).findByRole('alert')).textContent).toBe('This question is no longer open: it was answered or withdrawn (404 not_found).')
    expect(document.activeElement).toBe(send)
  })

  it('reads the inbox again on the subscriptions topic\'s inbox events', async () => {
    const { server, user } = renderInbox()
    await openInbox(user)
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: [
        'devices',
        'sessions',
        'agent',
        'media',
        'presence',
        'subscriptions',
        'alarms',
        'ops',
      ],
    }))
    const before = server.calls(INBOX_ROUTE)
    act(() => FakeSocket.last.receive({
      type: 'subscription_inbox',
      deviceId: ID,
      data: {
        ids: [3],
        action: 'answered',
      },
    }))
    await waitFor(() => expect(server.calls(INBOX_ROUTE)).toBe(before + 1))
  })

  it('says when nothing waits', async () => {
    const { user } = renderInbox({
      [INBOX_ROUTE]: json({ items: [] }),
    })
    const panel = await openInbox(user)
    expect(within(panel).getByText('Nothing waits for an answer.')).toBeTruthy()
    expect(within(panel).getByText('Nothing in the inbox')).toBeTruthy()
  })
})
