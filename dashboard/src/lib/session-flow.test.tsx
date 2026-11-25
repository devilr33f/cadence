// @vitest-environment jsdom
import { useQuery } from '@tanstack/react-query'
import { act, cleanup, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { listDevicesOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'
import { json, signedIn, signedOut } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function Home() {
  return <h1>Home</h1>
}

function DevicesProbe() {
  const api = useApi()
  const { data } = useQuery(sharedKey(listDevicesOptions({ client: api })))
  return (
    <h1>
      {data
        ? `${data.length} speakers`
        : 'Home'}
    </h1>
  )
}

// the session as the server sees it: tests end it as the server would
function session(initial: boolean) {
  const state = { authenticated: initial }
  return {
    state,
    route: () => Response.json(state),
  }
}

describe('the guard', () => {
  it('sends a reader without a session to the login, remembering where they were going', async () => {
    const { router } = renderApp({
      path: '/devices?tab=log',
      routes: { 'GET /session': signedOut },
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(router.state.location.pathname).toBe('/login')
    expect(router.state.location.search).toEqual({ redirect: '/devices?tab=log' })
    expect(FakeSocket.instances).toHaveLength(0)
  })

  it('lets a signed-in reader through, with the live feed and the account menu in the header', async () => {
    renderApp({
      path: '/devices',
      routes: { 'GET /session': signedIn },
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Devices' })
    const banner = screen.getByRole('banner')
    expect(within(banner).getByRole('button', { name: /Owner/ })).toBeTruthy()
    expect(within(banner).getByText('Connecting')).toBeTruthy()

    expect(FakeSocket.instances).toHaveLength(1)
    expect(FakeSocket.last.url).toBe(`ws://${window.location.host}/api/v1/live`)
    act(() => FakeSocket.last.open())
    expect(within(banner).getByText('Live')).toBeTruthy()
  })

  it('says at once, in codes, when the session cannot be checked', async () => {
    // react reports the error the route's boundary caught
    const reported = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { server } = renderApp({
      path: '/devices',
      routes: {
        'GET /session': json({ error: 'unavailable' }, 503),
      },
      home: Home,
    })
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('The page could not be shown')
    expect(alert.textContent).toContain('The server answered 503 unavailable.')
    expect(server.calls('GET /session')).toBe(1)
    expect(reported).toHaveBeenCalled()
    reported.mockRestore()
  })
})

describe('an ended session', () => {
  it('returns to the login when any query answers 401, and forgets every cached answer', async () => {
    const { state, route } = session(true)
    const { router, queryClient, onUnauthorized } = renderApp({
      path: '/',
      routes: {
        'GET /session': route,
        'GET /devices': () => {
          state.authenticated = false
          return Response.json({ error: 'unauthorized' }, { status: 401 })
        },
      },
      home: DevicesProbe,
    })
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(onUnauthorized).toHaveBeenCalledOnce()
    expect(router.state.location.search).toEqual({ redirect: '/' })
    expect(queryClient.getQueryCache().getAll()).toEqual([])
    expect(FakeSocket.last.closedWith).toBe(1000)
  })

  it('returns to the login when the feed closes 1008 for an ended session, without reconnecting', async () => {
    const { state, route } = session(true)
    const { router, server } = renderApp({
      path: '/devices',
      routes: { 'GET /session': route },
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Devices' })
    act(() => FakeSocket.last.open())

    state.authenticated = false
    act(() => FakeSocket.last.serverClose(1008, 'unauthorized'))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(router.state.location.search).toEqual({ redirect: '/devices' })
    expect(server.calls('GET /session')).toBeGreaterThanOrEqual(2)
    expect(FakeSocket.instances).toHaveLength(1)
  })
})

describe('signing out', () => {
  async function signOut() {
    const user = setupUser()
    await user.click(screen.getByRole('button', { name: /Owner/ }))
    await user.click(await screen.findByRole('menuitem', { name: 'Sign out' }))
  }

  it('ends the session, forgets the cache and shows the login', async () => {
    const { state, route } = session(true)
    const { router, server, queryClient } = renderApp({
      path: '/devices',
      routes: {
        'GET /session': route,
        'DELETE /session': () => {
          state.authenticated = false
          return new Response(null, { status: 204 })
        },
      },
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Devices' })
    await signOut()
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(server.calls('DELETE /session')).toBe(1)
    expect(server.requests.find(r => r.method === 'DELETE')?.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(router.state.location.search).toEqual({})
    expect(queryClient.getQueryCache().getAll()).toEqual([])
    expect(FakeSocket.last.closedWith).toBe(1000)
  })

  it('counts a 401 on sign-out as signed out', async () => {
    const { state, route } = session(true)
    const { onUnauthorized } = renderApp({
      path: '/devices',
      routes: {
        'GET /session': route,
        'DELETE /session': () => {
          state.authenticated = false
          return Response.json({ error: 'unauthorized' }, { status: 401 })
        },
      },
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Devices' })
    await signOut()
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('stays signed in and says why when the server cannot end the session', async () => {
    const { router } = renderApp({
      path: '/devices',
      routes: {
        'GET /session': signedIn,
        'DELETE /session': json({ error: 'unavailable' }, 503),
      },
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Devices' })
    await signOut()
    expect(await screen.findByText('Sign-out failed')).toBeTruthy()
    expect(screen.getByText('The server answered 503 unavailable.')).toBeTruthy()
    expect(router.state.location.pathname).toBe('/devices')
  })
})
