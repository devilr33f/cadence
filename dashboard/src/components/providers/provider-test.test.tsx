// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { CATALOG } from '@/test/provider-modules'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { ProvidersPage } from './providers-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const FLUX = {
  id: 'id-flux',
  name: 'flux',
  module: 'deepgram-flux',
  config: {},
  createdAt: '2026-11-10T08:15:00Z',
  secrets: {
    apiKey: { set: true },
  },
}

function renderProviders(test: FakeRoute) {
  const app = renderApp({
    path: '/providers',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /providers': json([FLUX]),
      'GET /providers/modules': json(CATALOG),
      'POST /providers/flux/test': test,
    },
    pages: { '/providers': ProvidersPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

function row(): HTMLElement {
  return screen.getByRole('row', { name: /flux/ })
}

async function testButton(): Promise<HTMLElement> {
  return screen.findByRole('button', { name: 'Test flux' })
}

function outcome(): string | null {
  return within(row()).getByRole('status').textContent
}

describe('testing a provider', () => {
  it('runs the test once however often it is pressed, keeps the focus on its button, and shows ok with the time', async () => {
    let answer!: (response: Response) => void
    const { server, user } = renderProviders(() => new Promise<Response>((resolve) => {
      answer = resolve
    }))
    const button = await testButton()
    expect(outcome()).toBe('')
    await user.click(button)
    expect(button.getAttribute('aria-disabled')).toBe('true')
    expect((button as HTMLButtonElement).disabled).toBe(false)
    expect(outcome()).toBe('Testing')
    await user.click(button)
    await user.click(button)
    answer(Response.json({ ok: true, ms: 132 }))
    await waitFor(() => expect(outcome()).toBe('OK132 ms'))
    expect(button.getAttribute('aria-disabled')).toBeNull()
    expect(document.activeElement).toBe(button)
    expect(server.calls('POST /providers/flux/test')).toBe(1)
    expect(server.requests.find(r => r.method === 'POST')!.headers.get('X-Cadence-CSRF')).toBe('1')
  })

  it.each([
    ['auth', 'Credential refusedauth', 'danger'],
    ['unreachable', 'Unreachableunreachable', 'danger'],
    ['timeout', 'Timed outtimeout', 'danger'],
    ['bad_response', 'Unexpected answerbad_response', 'danger'],
    ['unsupported', 'Cannot be testedunsupported', 'neutral'],
    ['rate_limited', 'Upstream rate limitedrate_limited', 'warning'],
    ['build_failed', 'Does not buildbuild_failed', 'danger'],
    ['some_new_code', 'Failedsome_new_code', 'danger'],
  ])('words the code %s from its table, with the code itself', async (code, text, tone) => {
    const { user } = renderProviders(json({ ok: false, code, ms: 40 }))
    await user.click(await testButton())
    await waitFor(() => expect(outcome()).toBe(text))
    expect(within(row()).getByRole('status').querySelector('[data-tone]')?.getAttribute('data-tone')).toBe(tone)
  })

  it('waits as long as the server\'s Retry-After says, the button held, then tests again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const answers = [
      () => Response.json({ error: 'rate_limited' }, {
        status: 429,
        headers: { 'Retry-After': '3' },
      }),
      () => Response.json({ ok: true, ms: 80 }),
    ]
    const { server, user } = renderProviders(() => answers.shift()!())
    const button = await testButton()
    await user.click(button)
    await waitFor(() => expect(outcome()).toBe('Wait3 s3 s before another test'))
    expect(button.getAttribute('aria-disabled')).toBe('true')
    await user.click(button)
    expect(server.calls('POST /providers/flux/test')).toBe(1)

    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(outcome()).toBe('Wait2 s3 s before another test')
    await act(() => vi.advanceTimersByTimeAsync(2000))
    await waitFor(() => expect(button.getAttribute('aria-disabled')).toBeNull())
    expect(outcome()).toBe('')
    await user.click(button)
    await waitFor(() => expect(outcome()).toBe('OK80 ms'))
    expect(server.calls('POST /providers/flux/test')).toBe(2)
  })

  it('holds the button for the server\'s own window when a 429 names no wait', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const { server, user } = renderProviders(json({ error: 'rate_limited' }, 429))
    const button = await testButton()
    await user.click(button)
    await waitFor(() => expect(outcome()).toBe('Wait5 s5 s before another test'))
    await act(() => vi.advanceTimersByTimeAsync(4000))
    await user.click(button)
    expect(server.calls('POST /providers/flux/test')).toBe(1)
    await act(() => vi.advanceTimersByTimeAsync(1000))
    await waitFor(() => expect(button.getAttribute('aria-disabled')).toBeNull())
  })

  it.each([
    [
      json({ error: 'not_found' }, 404),
      'Not tested404 not_found',
    ],
    [
      json({ error: 'internal' }, 500),
      'Not tested500 internal',
    ],
    [
(() => {
  throw new TypeError('Failed to fetch')
}) satisfies FakeRoute,
'Not testedno answer',
    ],
  ])('says a test that never ran in codes alone', async (route, text) => {
    const { user } = renderProviders(route)
    await user.click(await testButton())
    await waitFor(() => expect(outcome()).toBe(text))
  })
})
