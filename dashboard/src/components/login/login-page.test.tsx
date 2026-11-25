// @vitest-environment jsdom
import { cleanup, screen } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'

const PASSWORD = 'correct horse battery'

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

function loginServer(answer: (request: Request) => Response) {
  const state = { authenticated: false }
  return {
    'GET /session': () => Response.json(state),
    'POST /session': (request: Request) => {
      const response = answer(request)
      state.authenticated = response.status === 204
      return response
    },
  }
}

const accepted = () => new Response(null, { status: 204 })

async function signIn(password = PASSWORD) {
  const user = setupUser()
  await user.type(await screen.findByLabelText('Password'), password)
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
}

describe('signing in', () => {
  it('sends the password once, with the csrf header, then goes on to the page asked for', async () => {
    const { server, queryClient } = renderApp({
      path: '/login?redirect=%2Fdevices',
      routes: loginServer(accepted),
      home: Home,
    })
    await signIn()
    await screen.findByRole('heading', { name: 'Devices' })

    const login = server.requests.find(r => r.method === 'POST')!
    expect(login.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await login.clone().json()).toEqual({ password: PASSWORD })
    // never kept: no mutation cache entry holds it
    expect(queryClient.getMutationCache().getAll()).toEqual([])
  })

  it.each([
    'https://evil.example/',
    '//evil.example/',
  ])('refuses the redirect %s and goes home instead', async (target) => {
    const { router } = renderApp({
      path: `/login?redirect=${encodeURIComponent(target)}`,
      routes: loginServer(accepted),
      home: Home,
    })
    await signIn()
    await screen.findByRole('heading', { name: 'Home' })
    expect(router.state.location.href).toBe('/')
  })

  it('says the password was wrong inline, without the session handler or leaving the page', async () => {
    const { router, onUnauthorized } = renderApp({
      path: '/login',
      routes: loginServer(() => Response.json({ error: 'unauthorized' }, { status: 401 })),
      home: Home,
    })
    await signIn('not-the-password')
    expect((await screen.findByRole('alert')).textContent).toBe('Wrong password.')
    expect(screen.getByLabelText('Password').getAttribute('aria-invalid')).toBe('true')
    expect(onUnauthorized).not.toHaveBeenCalled()
    expect(router.state.location.pathname).toBe('/login')
    expect(document.body.textContent).not.toContain('not-the-password')
  })

  it('says how long to wait after too many attempts', async () => {
    renderApp({
      path: '/login',
      routes: loginServer(() => Response.json({ error: 'rate_limited' }, {
        status: 429,
        headers: { 'Retry-After': '120' },
      })),
      home: Home,
    })
    await signIn()
    expect((await screen.findByRole('alert')).textContent).toBe('Too many attempts, try again in 120 s.')
  })

  it('names what the server answered otherwise, in codes', async () => {
    renderApp({
      path: '/login',
      routes: loginServer(() => Response.json({ error: 'csrf' }, { status: 403 })),
      home: Home,
    })
    await signIn()
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 403 csrf.')
  })

  it('sends a reader who is already signed in on to the page asked for', async () => {
    renderApp({
      path: '/login?redirect=%2Fdevices',
      routes: { 'GET /session': signedIn },
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Devices' })
  })

  // a submit before hydration is the browser's own: a get would put the password in the url (history, any proxy's
  // access log); a post to /login is answered 405 by the image's server, its body unread
  it('renders a form that posts to the login page', async () => {
    renderApp({
      path: '/login?redirect=%2Fdevices',
      routes: loginServer(accepted),
      home: Home,
    })
    const form = (await screen.findByLabelText('Password')).closest('form')!
    expect(form.getAttribute('method')).toBe('post')
    expect(form.getAttribute('action')).toBe('/login')
  })

  it('has no accessibility violations axe can see without layout', async () => {
    renderApp({
      path: '/login',
      routes: loginServer(accepted),
      home: Home,
    })
    await screen.findByRole('heading', { name: 'Sign in' })
    const results = await axe.run(document.body, {
      rules: {
        'color-contrast': { enabled: false },
      },
    })
    expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  })
})
