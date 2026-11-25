// @vitest-environment jsdom
import { useQuery } from '@tanstack/react-query'
import { act, cleanup, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { listMemoriesOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { queryOf } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { useLiveTopics } from './use-live'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const AGENT = ['agent'] as const
const TASKS = ['tasks'] as const

// a page that follows the agent topic and reads what it feeds, as the memory page does
function MemoryReader() {
  useLiveTopics(AGENT)
  const api = useApi()
  const memories = useQuery(sharedKey(listMemoriesOptions({
    client: api,
    query: { status: 'active' },
  })))
  return <h1>{memories.isSuccess ? 'Memories read' : 'Reading'}</h1>
}

function TasksReader() {
  useLiveTopics(TASKS)
  return <h1>Tasks</h1>
}

it('reads again what a topic feeds when a page brings it back into the subscription, fresh cache or not', async () => {
  const { server, router } = renderApp({
    path: '/memory',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /memories': json([]),
    },
    pages: { '/memory': MemoryReader, '/tasks': TasksReader },
  })
  const reads = () => server.requests.filter(r => new URL(r.url).pathname === '/api/v1/memories'
    && queryOf(r).status === 'active').length
  await screen.findByRole('heading', { name: 'Memories read' })
  act(() => FakeSocket.last.open())
  await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
    subscribe: ['agent', 'presence'],
  }))
  expect(reads()).toBe(1)

  // away: the agent topic leaves, and its events (a suggestion approved elsewhere) are not delivered
  await act(() => router.navigate({ to: '/tasks' }))
  await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
    subscribe: ['tasks', 'presence'],
  }))

  // back within the 30 s the cache stays fresh: the join reads it again
  await act(() => router.navigate({ to: '/memory' }))
  await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
    subscribe: ['agent', 'presence'],
  }))
  await waitFor(() => expect(reads()).toBe(2))
})
