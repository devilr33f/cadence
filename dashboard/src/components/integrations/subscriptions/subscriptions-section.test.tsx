// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, noContent } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { BEDROOM, KITCHEN, queryOf } from '@/test/household'
import { integrationRoutes } from '@/test/integrations'
import { stubColorScheme } from '@/test/media'
import { HOSTILE_TITLE, PROJECT_ENTRIES, SUBSCRIPTIONS, TOP_ENTRIES } from '@/test/subscriptions'
import { renderApp } from '@/test/test-app'
import { enter, setupUser } from '@/test/typing'
import { IntegrationsPage } from '../integrations-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const CREATE = 'POST /subscriptions'

// what the module lists at each prefix
const topics: FakeRoute = (request) => {
  const prefix = queryOf(request).prefix
  if (prefix === 't3code:work')
    return Response.json({ entries: TOP_ENTRIES })
  if (prefix === 't3code:work:project:p1')
    return Response.json({ entries: PROJECT_ENTRIES })
  return Response.json({ error: 'invalid_topic', path: '/query/prefix' }, { status: 422 })
}

function renderSection(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/integrations',
    routes: integrationRoutes({
      'GET /subscriptions/topics': topics,
      ...routes,
    }),
    pages: { '/integrations': IntegrationsPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

async function section() {
  const region = await screen.findByRole('region', { name: 'Subscriptions' })
  await within(region).findByRole('button', { name: /^Remove: t3code:work:\*/ })
  return region
}

function rows(region: HTMLElement): string[][] {
  const table = within(region).getByRole('table', { name: 'Subscriptions' })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 7).map(cell => cell.textContent ?? ''))
}

async function bodyOf(server: ReturnType<typeof renderApp>['server'], key: string, index = 0) {
  const requests = server.requests.filter(r => `${r.method} ${new URL(r.url).pathname.replace(/^\/api\/v1/, '')}` === key)
  return requests[index]!.clone().json() as Promise<unknown>
}

describe('the subscriptions', () => {
  it('lists every speaker\'s, newest first, and one speaker\'s through the filter', async () => {
    const { user } = renderSection()
    const region = await section()
    expect(rows(region)).toEqual([
      [
        'Bedroom',
        't3code:work:thread:th-9',
        'StoppedQuestion',
        'Muted',
        'Until the next stop',
        'Dashboard or API',
        expect.any(String),
      ],
      [
        'Kitchen',
        't3code:work:*',
        'Every event',
        'Spoken',
        'Until removed',
        'Voice',
        expect.any(String),
      ],
    ])
    await user.selectOptions(within(region).getByRole('combobox', { name: 'Speaker' }), 'Kitchen')
    expect(rows(region).map(row => row[0])).toEqual(['Kitchen'])
  })

  it('follows a topic browsed from a provider down, with the events, delivery and end chosen', async () => {
    const { server, user } = renderSection({
      [CREATE]: json(SUBSCRIPTIONS[0], 201),
    })
    const region = await section()
    await user.click(within(region).getByRole('button', { name: 'Follow a topic' }))
    const dialog = await screen.findByRole('dialog', { name: 'Follow a topic' })
    await user.selectOptions(within(dialog).getByRole('combobox', { name: /^Speaker/ }), 'Bedroom')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: 'Provider' }), 'work (t3code)')

    const list = await within(dialog).findByRole('list', { name: 'What the module lists' })
    // a title is the outside system's own: text, never markup
    expect(within(list).getByText(HOSTILE_TITLE)).toBeTruthy()
    expect(dialog.querySelector('b, script')).toBeNull()
    await user.click(within(list).getByRole('button', { name: 'Open: Cadence' }))
    const project = await within(dialog).findByRole('list', { name: 'What the module lists' })
    await within(project).findByText('Dashboard pages')
    await user.click(within(project).getByRole('button', { name: 'Choose: Dashboard pages' }))
    const topic = within(dialog).getByRole('textbox', { name: /^Topic/ })
    expect((topic as HTMLInputElement).value).toBe('t3code:work:thread:th-2')
    expect(document.activeElement).toBe(topic)

    await user.click(within(dialog).getByRole('checkbox', { name: 'Stopped' }))
    await user.click(within(dialog).getByRole('checkbox', { name: 'Question' }))
    await user.click(within(dialog).getByRole('radio', { name: /^Agent turn/ }))
    await user.click(within(dialog).getByRole('radio', { name: /^Until the next stop/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Follow the topic' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Follow a topic' })).toBeNull())
    expect(await bodyOf(server, CREATE)).toEqual({
      device: BEDROOM,
      topic: 't3code:work:thread:th-2',
      events: ['stopped', 'question'],
      delivery: 'agent',
      until: 'next_stop',
    })
  })

  it('takes a topic typed by hand, leaving the delivery and events to the server', async () => {
    const { server, user } = renderSection({
      [CREATE]: json(SUBSCRIPTIONS[0], 201),
    })
    const region = await section()
    await user.click(within(region).getByRole('button', { name: 'Follow a topic' }))
    const dialog = await screen.findByRole('dialog', { name: 'Follow a topic' })
    await enter(user, within(dialog).getByRole('textbox', { name: /^Topic/ }), 't3code:work:*')
    await user.click(within(dialog).getByRole('button', { name: 'Follow the topic' }))
    await waitFor(() => expect(server.calls(CREATE)).toBe(1))
    expect(await bodyOf(server, CREATE)).toEqual({ device: KITCHEN, topic: 't3code:work:*', until: 'unsubscribed' })
  })

  it('says what is wrong with a topic before asking, and the server\'s refusals by code on their fields', async () => {
    let answer: { error: string, path: string } = { error: 'unknown_module', path: '/body/topic' }
    const { server, user } = renderSection({
      [CREATE]: () => Response.json(answer, { status: 422 }),
    })
    const region = await section()
    await user.click(within(region).getByRole('button', { name: 'Follow a topic' }))
    const dialog = await screen.findByRole('dialog', { name: 'Follow a topic' })
    const topic = within(dialog).getByRole('textbox', { name: /^Topic/ })
    const submit = within(dialog).getByRole('button', { name: 'Follow the topic' })

    await enter(user, topic, 't3code:*:x')
    await user.click(submit)
    expect(within(dialog).getByText('The * may only be the last part.')).toBeTruthy()
    expect(document.activeElement).toBe(topic)
    expect(server.calls(CREATE)).toBe(0)

    await user.clear(topic)
    await enter(user, topic, 'nope:work:*')
    await user.click(submit)
    expect(await within(dialog).findByText('No module of this name can be followed (unknown_module).')).toBeTruthy()
    await expectFocusOn(topic)

    answer = { error: 'unknown_instance', path: '/body/topic' }
    await user.click(submit)
    expect(await within(dialog).findByText('The module has no provider of this name (unknown_instance).')).toBeTruthy()

    answer = { error: 'invalid_topic', path: '/body/topic' }
    await user.click(submit)
    expect(await within(dialog).findByText(/\(invalid_topic\)\.$/)).toBeTruthy()

    answer = { error: 'unsupported_event', path: '/body/events' }
    await user.click(within(dialog).getByRole('checkbox', { name: 'Changed' }))
    await user.click(submit)
    expect(await within(dialog).findByText('The module sends none of some of these events (unsupported_event).')).toBeTruthy()
    await expectFocusOn(within(dialog).getByRole('checkbox', { name: 'Stopped' }))
  })

  it('removes one after asking, and reads the list again', async () => {
    const { server, user } = renderSection({
      [`DELETE /subscriptions/${SUBSCRIPTIONS[1]!.id}`]: noContent(),
    })
    const region = await section()
    const before = server.calls('GET /subscriptions')
    await user.click(within(region).getByRole('button', { name: 'Remove: t3code:work:thread:th-9 on Bedroom' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Stop following this topic?' })
    await user.click(within(dialog).getByRole('button', { name: 'Remove the subscription' }))
    await waitFor(() => expect(server.calls(`DELETE /subscriptions/${SUBSCRIPTIONS[1]!.id}`)).toBe(1))
    await waitFor(() => expect(server.calls('GET /subscriptions')).toBeGreaterThan(before))
    await expectFocusOn(within(region).getByRole('button', { name: 'Follow a topic' }))
  })

  it('reads the list again when a subscription changes elsewhere', async () => {
    const { server } = renderSection()
    await section()
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: ['agent', 'presence', 'subscriptions'],
    }))
    const before = server.calls('GET /subscriptions')
    act(() => FakeSocket.last.receive({
      type: 'subscription_changed',
      deviceId: KITCHEN,
      data: { id: SUBSCRIPTIONS[0]!.id, topic: 't3code:work:*', action: 'ended' },
    }))
    await waitFor(() => expect(server.calls('GET /subscriptions')).toBe(before + 1))
  })
})
