import type { UserEvent } from '@testing-library/user-event'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient } from '@/api/client'
import { boardsQueryOptions, devicesQueryOptions } from '@/lib/devices'
import { json, noContent, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
// @vitest-environment jsdom
import { expectFocusOn } from '@/test/focus'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { enter, setupUser } from '@/test/typing'
import { EnrollCode } from './enroll-code'
import { EnrollPage } from './enroll-page'

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
})

const ID = '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7aa'
const CODE = 'K7Q2-9XPL'

function created(expiresAt = new Date(Date.now() + 10 * 60_000).toISOString()): FakeRoute {
  return () => Response.json({
    id: ID,
    name: 'Kitchen',
    model: 'sim',
    status: 'pending',
    code: CODE,
    codeExpiresAt: expiresAt,
  }, { status: 201 })
}

function detail(status: string, online: boolean) {
  return {
    id: ID,
    name: 'Kitchen',
    model: 'sim',
    status,
    online,
    lastSeenAt: null,
    capabilities: [],
  }
}

function speaker(...answers: object[]): FakeRoute {
  let n = 0
  return () => Response.json(answers[Math.min(n++, answers.length - 1)])
}

function routes(overrides: Record<string, FakeRoute> = {}): Record<string, FakeRoute> {
  return {
    'GET /session': signedIn,
    'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
    // as enroll.Boards lists them
    'GET /boards': json([
      { id: 'sim', name: 'Simulator' },
      { id: 'yandex-pickle', name: 'Yandex pickle' },
      { id: 'yandex-midi', name: 'Yandex midi' },
    ]),
    'POST /devices': created(),
    [`GET /devices/${ID}`]: speaker(detail('pending', false), detail('enrolled', true)),
    ...overrides,
  }
}

function renderEnroll(server = routes()) {
  return renderApp({
    path: '/devices/enroll',
    routes: server,
    pages: {
      '/devices/enroll': EnrollPage,
      '/devices/$id': () => <h1>Kitchen&apos;s page</h1>,
    },
  })
}

async function makeCode(user: UserEvent, board = 'Simulator') {
  await user.selectOptions(await screen.findByLabelText('Board'), board)
  await user.type(screen.getByLabelText(/^Name/), 'Kitchen')
  await user.click(screen.getByRole('button', { name: 'Make an enroll code' }))
}

describe('enrolling a speaker', () => {
  it('makes a code for the board and name, shows the command to run, and waits for the speaker to come online', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    const { server } = renderEnroll()
    await makeCode(user)

    const post = server.requests.find(r => r.method === 'POST')!
    expect(await post.clone().json()).toEqual({ model: 'sim', name: 'Kitchen' })
    expect(post.headers.get('X-Cadence-CSRF')).toBe('1')

    expect(await screen.findByText(CODE)).toBeTruthy()
    // the form that held focus is gone: the next step takes it
    await expectFocusOn(screen.getByRole('heading', { name: 'Step 2: Run the command' }))
    const command = screen.getByRole('figure', { name: 'Command to run' }).querySelector('code')
    expect(command?.textContent).toBe(
      `cadenced enroll --server ${window.location.origin} --code ${CODE} --board sim --config /var/lib/cadence/config.json --data-dir /var/lib/cadence`,
    )
    expect(screen.getByRole('status', { name: 'Speaker' }).textContent).toContain('Waiting for Kitchen')

    await act(() => vi.advanceTimersByTimeAsync(2000))
    await waitFor(() => expect(screen.getByRole('status', { name: 'Speaker' }).textContent).toContain('Kitchen is online.'))
    // the code is single use: once the speaker enrolled it is no longer shown
    expect(screen.queryByText(CODE)).toBeNull()
    await user.click(screen.getByRole('link', { name: 'Open Kitchen' }))
    expect(await screen.findByRole('heading', { name: 'Kitchen\'s page' })).toBeTruthy()
  })

  it('says why the speaker could not be read, in codes, and stops asking', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    const { server } = renderEnroll(routes({
      [`GET /devices/${ID}`]: json({ error: 'forbidden' }, 403),
    }))
    await makeCode(user)
    expect((await screen.findByRole('alert')).textContent).toBe('The speaker could not be read. The server answered 403 forbidden.')
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(server.calls(`GET /devices/${ID}`)).toBe(1)
  })

  it('stops asking once the code expired unused, and offers a new one with the form focused', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    const { server } = renderEnroll(routes({
      'POST /devices': created(new Date(Date.now() + 3000).toISOString()),
      [`GET /devices/${ID}`]: json(detail('pending', false)),
    }))
    await makeCode(user)
    await screen.findByText(CODE)
    await act(() => vi.advanceTimersByTimeAsync(4000))
    expect(screen.getByRole('alert').textContent).toContain('The code expired.')
    // a read well after the expiry settles it: the step stops waiting, its spinner gone
    await act(() => vi.advanceTimersByTimeAsync(4000))
    const speakerStatus = screen.getByRole('status', { name: 'Speaker' })
    expect(speakerStatus.textContent).toBe('The code expired before Kitchen enrolled.')
    expect(speakerStatus.querySelector('svg')).toBeNull()
    const reads = server.calls(`GET /devices/${ID}`)
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    expect(server.calls(`GET /devices/${ID}`)).toBe(reads)

    await user.click(screen.getByRole('button', { name: 'Make a new code' }))
    const name = screen.getByLabelText(/^Name/) as HTMLInputElement
    expect(name.value).toBe('Kitchen')
    expect(document.activeElement).toBe(name)
  })

  it('follows a speaker that used its code in the last second, after the last read before the expiry', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    let state = detail('pending', false)
    const { queryClient } = renderEnroll(routes({
      'POST /devices': created(new Date(Date.now() + 3000).toISOString()),
      [`GET /devices/${ID}`]: () => Response.json(state),
    }))
    await makeCode(user)
    await screen.findByText(CODE)
    // the reads at 0 and 2 s found it pending; it enrolls at 2.5 s
    await act(() => vi.advanceTimersByTimeAsync(2500))
    state = detail('enrolled', false)
    // a render after the expiry (3 s), before the next read (4 s): the boards read again, say. the poll must not end
    // on the read from before the expiry
    await act(() => vi.advanceTimersByTimeAsync(1000))
    act(() => queryClient.setQueryData(boardsQueryOptions(createApiClient()).queryKey, [
      { id: 'sim', name: 'Simulator' },
      { id: 'yandex-midi', name: 'Yandex Station Max (midi)' },
    ]))
    await act(() => vi.advanceTimersByTimeAsync(2500))
    expect(screen.getByText('Kitchen used its code; it is no longer valid.')).toBeTruthy()
    expect(screen.queryByText('The code expired.')).toBeNull()
    expect(screen.getByRole('status', { name: 'Speaker' }).textContent).toContain('Kitchen enrolled; waiting for it to connect')
    state = detail('enrolled', true)
    await act(() => vi.advanceTimersByTimeAsync(2000))
    await waitFor(() => expect(screen.getByRole('status', { name: 'Speaker' }).textContent).toContain('Kitchen is online.'))
  })

  it.each([
    [
      'yandex-pickle',
      'Yandex pickle',
      '',
      'deploy/pickle/README.md',
    ],
    [
      'yandex-midi',
      'Yandex midi',
      ' --ota-blocked',
      'deploy/midi/README.md',
    ],
  ])('shows the installer\'s command for %s, as its readme runs it', async (model, board, flags, readme) => {
    const user = setupUser()
    const { server } = renderEnroll(routes({
      'POST /devices': () => Response.json({
        id: ID,
        name: 'Kitchen',
        model,
        status: 'pending',
        code: CODE,
        codeExpiresAt: new Date(Date.now() + 600_000).toISOString(),
      }, { status: 201 }),
    }))
    await makeCode(user, board)
    const figure = await screen.findByRole('figure', { name: 'Command to run' })
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({ name: 'Kitchen', model })
    expect(figure.querySelector('pre code')?.textContent).toBe(
      `build/cadence-install.exe $speaker --tarball "$tarball" --server ${window.location.origin}${flags} --code ${CODE}`,
    )
    // where the line's variables come from
    expect(figure.textContent).toContain(`From the repo root, with $speaker and $tarball as ${readme} sets them.`)
  })

  it('copies the command', async () => {
    const user = setupUser()
    renderEnroll()
    await makeCode(user)
    await user.click(await screen.findByRole('button', { name: 'Copy the command' }))
    expect(await navigator.clipboard.readText()).toContain(`--code ${CODE}`)
    expect(screen.getByRole('button', { name: 'Copied' })).toBeTruthy()
  })

  it('keeps the code out of the logs, the storage and the query cache', async () => {
    const spies = ([
      'log',
      'info',
      'warn',
      'error',
      'debug',
    ] as const).map(level => vi.spyOn(console, level))
    const user = setupUser()
    const { queryClient } = renderEnroll()
    await makeCode(user)
    await screen.findByText(CODE)

    for (const spy of spies)
      expect(JSON.stringify(spy.mock.calls)).not.toContain(CODE)
    const stored = [localStorage, sessionStorage].flatMap(s => Object.keys(s).map(k => s.getItem(k) ?? ''))
    expect(stored.join('')).not.toContain(CODE)
    const cached = queryClient.getQueryCache().getAll().map(q => JSON.stringify(q.state.data) ?? '')
    expect(cached.join('')).not.toContain(CODE)
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0)
  })

  it('names what is missing before it asks the server', async () => {
    const user = setupUser()
    const { server } = renderEnroll()
    await screen.findByLabelText('Board')
    await user.click(screen.getByRole('button', { name: 'Make an enroll code' }))
    expect(screen.getByRole('alert').textContent).toBe('Give the speaker a name.')
    expect(server.calls('POST /devices')).toBe(0)
  })

  it('holds a name to the server\'s 64 characters', async () => {
    const user = setupUser()
    const { server } = renderEnroll()
    await enter(user, await screen.findByLabelText(/^Name/), 'k'.repeat(65))
    await user.click(screen.getByRole('button', { name: 'Make an enroll code' }))
    expect(screen.getByRole('alert').textContent).toBe('At most 64 characters.')
    expect(server.calls('POST /devices')).toBe(0)
    await user.type(screen.getByLabelText(/^Name/), '{Backspace}')
    await user.click(screen.getByRole('button', { name: 'Make an enroll code' }))
    await waitFor(() => expect(server.calls('POST /devices')).toBe(1))
  })

  it('keeps the focus on its button while the code is made and after a refusal', async () => {
    const user = setupUser()
    let answer!: (response: Response) => void
    const { server } = renderEnroll(routes({
      'POST /devices': () => new Promise<Response>((resolve) => {
        answer = resolve
      }),
    }))
    await makeCode(user)
    await waitFor(() => expect(server.calls('POST /devices')).toBe(1))
    const make = screen.getByRole('button', { name: 'Make an enroll code' })
    expect(make.getAttribute('aria-disabled')).toBe('true')
    expect((make as HTMLButtonElement).disabled).toBe(false)
    await user.click(make)
    expect(server.calls('POST /devices')).toBe(1)
    answer(Response.json({ error: 'name_taken' }, { status: 409 }))
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 409 name_taken.')
    expect(document.activeElement).toBe(make)
    expect(make.getAttribute('aria-disabled')).toBeNull()
  })

  it('says why the server refused, in codes', async () => {
    const user = setupUser()
    renderEnroll(routes({
      'POST /devices': json({ error: 'unknown_model' }, 422),
    }))
    await makeCode(user)
    expect((await screen.findByRole('alert')).textContent).toBe('The server answered 422 unknown_model.')
  })

  it.each([
    [
      'made',
      created(),
    ],
    // an answer lost on its way may still have made the device
    [
      'refused',
      json({ error: 'internal' }, 500),
    ],
  ])('marks the devices list stale once the server answered (%s)', async (_, answer) => {
    const user = setupUser()
    const { queryClient, server } = renderEnroll(routes({
      'POST /devices': answer,
    }))
    const list = devicesQueryOptions(createApiClient()).queryKey
    queryClient.setQueryData(list, [])
    await makeCode(user)
    await waitFor(() => expect(server.calls('POST /devices')).toBe(1))
    await waitFor(() => expect(queryClient.getQueryState(list)?.isInvalidated).toBe(true))
  })
})

describe('an expired code\'s pending device', () => {
  async function expire(overrides: Record<string, FakeRoute>) {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = setupUser({ advanceTimers: vi.advanceTimersByTime })
    const app = renderEnroll(routes({
      'POST /devices': created(new Date(Date.now() + 3000).toISOString()),
      [`GET /devices/${ID}`]: json(detail('pending', false)),
      ...overrides,
    }))
    await makeCode(user)
    await screen.findByText(CODE)
    await act(() => vi.advanceTimersByTimeAsync(4000))
    expect(screen.getByRole('alert').textContent).toContain('The code expired.')
    return { ...app, user }
  }

  it('is removed on request, so its name is free again, and the form opens on it', async () => {
    const { server, user, queryClient } = await expire({
      [`DELETE /devices/${ID}`]: noContent(),
    })
    const list = devicesQueryOptions(createApiClient()).queryKey
    queryClient.setQueryData(list, [])
    await user.click(screen.getByRole('button', { name: 'Remove Kitchen' }))

    const name = await screen.findByLabelText(/^Name/) as HTMLInputElement
    expect(name.value).toBe('Kitchen')
    expect(document.activeElement).toBe(name)
    expect(screen.getByRole('status', { name: 'Removed' }).textContent).toBe('Kitchen was removed: its name is free again.')
    const removal = server.requests.find(r => r.method === 'DELETE')!
    expect(new URL(removal.url).pathname).toBe(`/api/v1/devices/${ID}`)
    // pending only: the server refuses, and deletes nothing, once the speaker enrolled
    expect(new URL(removal.url).search).toBe('?status=pending')
    expect(removal.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(queryClient.getQueryState(list)?.isInvalidated).toBe(true)
  })

  it('follows a speaker that enrolled between its read and the delete, which the server refused', async () => {
    let enrolled = false
    const { server, user } = await expire({
      [`GET /devices/${ID}`]: () => Response.json(detail(enrolled ? 'enrolled' : 'pending', false)),
      [`DELETE /devices/${ID}`]: () => {
        enrolled = true
        return Response.json({ error: 'not_pending' }, { status: 409 })
      },
    })
    // the poll's reads have ended: only the refused delete can tell the page the speaker enrolled
    await act(() => vi.advanceTimersByTimeAsync(2000))
    expect(screen.getByRole('status', { name: 'Speaker' }).textContent).toBe('The code expired before Kitchen enrolled.')
    await user.click(screen.getByRole('button', { name: 'Remove Kitchen' }))
    expect(await screen.findByText('Kitchen used its code; it is no longer valid.')).toBeTruthy()
    expect(server.calls(`DELETE /devices/${ID}`)).toBe(1)
    expect(screen.getByRole('status', { name: 'Speaker' }).textContent).toContain('Kitchen enrolled; waiting for it to connect')
    expect(screen.queryByText(/was not removed/)).toBeNull()
    expect(screen.queryByRole('status', { name: 'Removed' })).toBeNull()
  })

  it('is left alone when it enrolled after all, in the last moments of its code', async () => {
    let enrolled = false
    const { server, user } = await expire({
      [`GET /devices/${ID}`]: () => Response.json(detail(enrolled ? 'enrolled' : 'pending', false)),
      [`DELETE /devices/${ID}`]: noContent(),
    })
    // the poll's last read came before the speaker enrolled; the read before a removal sees it
    enrolled = true
    await user.click(screen.getByRole('button', { name: 'Remove Kitchen' }))
    expect(await screen.findByText('Kitchen used its code; it is no longer valid.')).toBeTruthy()
    expect(server.calls(`DELETE /devices/${ID}`)).toBe(0)
    expect(screen.getByRole('status', { name: 'Speaker' }).textContent).toContain('Kitchen enrolled; waiting for it to connect')
  })

  it('says why it was not removed, and keeps the focus and the way to a new code', async () => {
    const { user } = await expire({
      [`DELETE /devices/${ID}`]: json({ error: 'internal' }, 500),
    })
    const remove = screen.getByRole('button', { name: 'Remove Kitchen' })
    await user.click(remove)
    expect((await screen.findByText(/^Kitchen was not removed/)).textContent).toBe('Kitchen was not removed. The server answered 500 internal.')
    expect(document.activeElement).toBe(remove)
    expect(screen.getByRole('button', { name: 'Make a new code' })).toBeTruthy()
  })
})

describe('the code\'s expiry', () => {
  it('counts down each second and says when the code expired, with a way to make a new one', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-12T15:00:00Z'))
    const onRestart = vi.fn()
    render(<EnrollCode code={CODE} expiresAt="2026-11-12T15:01:05Z" hint={{ command: 'cmd' }} onRestart={onRestart} />)
    const left = () => screen.getByText(/left$/).textContent
    expect(left()).toBe('1:05 left')
    act(() => vi.advanceTimersByTime(5000))
    expect(left()).toBe('1:00 left')
    act(() => vi.advanceTimersByTime(60_000))
    const expired = screen.getByRole('alert')
    expect(expired.textContent).toContain('The code expired.')
    expect(screen.queryByText(CODE)).toBeNull()
    act(() => within(expired).getByRole('button', { name: 'Make a new code' }).click())
    expect(onRestart).toHaveBeenCalled()
  })
})
