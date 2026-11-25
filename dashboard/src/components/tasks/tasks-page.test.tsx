// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { BEDROOM, DEVICES, queryOf } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { task } from '@/test/tasks'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { TasksPage } from './tasks-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const BOOK = task({
  shortId: 't-3',
  title: 'Book a table for two',
  status: 'waiting_input',
  question: {
    kind: 'clarify',
    text: 'Which day?',
    options: [],
    token: 'q3',
  },
})
const REPORT = task({
  shortId: 't-2',
  title: 'Summarize the news',
  status: 'running',
  progress: 'reading 3 of 5',
  deviceId: BEDROOM,
  createdAt: '2026-11-11T21:40:00Z',
})
const SHOP = task({
  shortId: 't-1',
  title: 'Order the groceries',
  status: 'failed',
  errorCode: 'kind_failed',
  deviceId: null,
  createdAt: '2026-11-10T08:15:00Z',
})

// the tasks the server holds, narrowed by the status filter as the server does
function tasksRoute(list = [BOOK, REPORT, SHOP]): FakeRoute {
  return (request) => {
    const statuses = queryOf(request).status?.split(',')
    const limit = Number(queryOf(request).limit ?? 50)
    return Response.json(list.filter(t => !statuses || statuses.includes(t.status)).slice(0, limit))
  }
}

function renderTasks(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/tasks',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /devices': json(DEVICES),
      'GET /tasks': tasksRoute(),
      ...routes,
    },
    pages: {
      '/tasks': TasksPage,
      '/tasks/$id': () => <h1>Task</h1>,
    },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function rows(): string[][] {
  const table = screen.getByRole('table', { name: 'Tasks' })
  return within(table).getAllByRole('row').slice(1).map(row => within(row).getAllByRole('cell').slice(0, 5).map(cell => cell.textContent ?? ''))
}

describe('the tasks list', () => {
  it('shows each task\'s kind, status, speaker and start, newest first', async () => {
    renderTasks()
    await screen.findByText('Book a table for two')
    await waitFor(() => expect(rows()[0]?.[3]).toBe('Kitchen'))
    expect(rows()).toEqual([
      [
        'Book a table for twot-3',
        'hermes',
        'Waiting for an answer',
        'Kitchen',
        '12 Nov, 15:00',
      ],
      [
        'Summarize the newst-2',
        'hermes',
        'Running',
        'Bedroom',
        '12 Nov, 03:40',
      ],
      [
        'Order the groceriest-1',
        'hermes',
        'Failedkind_failed',
        '—',
        '10 Nov, 14:15',
      ],
    ])
    expect(screen.getByRole('link', { name: 'Book a table for two' }).getAttribute('href')).toBe('/tasks/t-3')
  })

  it('narrows them by status, as the server filters them', async () => {
    const { server, user } = renderTasks()
    await screen.findByText('Book a table for two')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Status' }), 'Waiting for an answer')
    await waitFor(() => expect(rows().map(r => r[0])).toEqual(['Book a table for twot-3']))
    expect(queryOf(server.requests.filter(r => r.url.includes('/tasks?')).at(-1)!).status).toBe('waiting_input')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Status' }), 'Active')
    await waitFor(() => expect(rows()).toHaveLength(2))
    expect(queryOf(server.requests.filter(r => r.url.includes('/tasks?')).at(-1)!).status).toBe('queued,running,waiting_input,unknown')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Status' }), 'Cancelled')
    expect(await screen.findByText('No tasks in this state')).toBeTruthy()
  })

  it('follows the tasks topic: a task event reads the list again, once for a burst', async () => {
    const { server } = renderTasks()
    await screen.findByText('Book a table for two')
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toEqual([
      {
        subscribe: ['tasks', 'presence'],
      },
    ]))
    const before = server.calls('GET /tasks')
    act(() => {
      FakeSocket.last.receive({
        type: 'task_event',
        data: { shortId: 't-2', status: 'done', seq: 4 },
      })
      FakeSocket.last.receive({
        type: 'task_event',
        data: { shortId: 't-2', status: 'done', seq: 5 },
      })
    })
    await waitFor(() => expect(server.calls('GET /tasks')).toBe(before + 1))
  })

  it('teaches what a task is when there is none', async () => {
    renderTasks({
      'GET /tasks': json([]),
    })
    expect(await screen.findByText('No tasks yet')).toBeTruthy()
  })

  it('says why the list could not be read, in codes', async () => {
    renderTasks({
      'GET /tasks': json({ error: 'forbidden' }, 403),
    })
    expect(await screen.findByText('The server answered 403 forbidden.')).toBeTruthy()
  })

  it('asks for more once the list is full', async () => {
    const many = Array.from({ length: 50 }, (_, i) => task({ shortId: `t-${100 + i}`, title: `Job ${i}`, status: 'done' }))
    const { server, user } = renderTasks({
      'GET /tasks': tasksRoute(many),
    })
    await screen.findByText('Job 0')
    await user.click(screen.getByRole('button', { name: 'Show more tasks' }))
    await waitFor(() => expect(queryOf(server.requests.filter(r => r.url.includes('/tasks?')).at(-1)!).limit).toBe('100'))
  })

  it('has no accessibility violations axe can see without layout', async () => {
    renderTasks()
    await screen.findByText('Book a table for two')
    const results = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  })
})
