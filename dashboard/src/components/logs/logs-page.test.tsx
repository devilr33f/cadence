// @vitest-environment jsdom
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { justAfter } from '@/lib/log-paging'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { expectFocusOn } from '@/test/focus'
import { BEDROOM, DEVICES, KITCHEN, queryOf } from '@/test/household'
import { scrollViewport, sendLine, sendLines, stored, STORED, stubLogLayout, VIEWPORT_PX } from '@/test/logs'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { LogsPage } from './logs-page'

// jsdom lays nothing out: every log box here is 200px tall, its lines 20px each
let restoreLayout: () => void

beforeEach(() => {
  restoreLayout = stubLogLayout()
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  restoreLayout()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

function renderLogs(logs: FakeRoute = json({ logs: STORED })) {
  const app = renderApp({
    path: '/logs',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /devices': json(DEVICES),
      'GET /logs': logs,
    },
    pages: { '/logs': LogsPage },
  })
  return {
    ...app,
    user: setupUser(),
    reads: () => app.server.requests.filter(r => new URL(r.url).pathname === '/api/v1/logs').map(queryOf),
  }
}

function lines(name: string) {
  return within(screen.getByRole('region', { name })).queryAllByRole('listitem')
}

function shownCount(name: string): number {
  return Number(lines(name)[0]?.getAttribute('aria-setsize') ?? 0)
}

describe('the logs page', () => {
  it('reads the newest stored lines, each as text with its time, level, speaker and source', async () => {
    const { reads } = renderLogs()
    await screen.findByRole('heading', { level: 1, name: 'Logs' })
    await screen.findByText('config_apply_failed code=<b>bad</b>')
    expect(reads()).toEqual([
      { limit: '200' },
    ])

    const [failed, ended, skipped, server] = lines('Stored lines')
    expect(within(failed!).getByText('21:04:13.517')).toBeTruthy()
    expect(within(failed!).getByText('ERROR')).toBeTruthy()
    expect(within(failed!).getByText('Kitchen')).toBeTruthy()
    expect(within(failed!).getByText('cadenced')).toBeTruthy()
    expect(ended!.textContent).toContain('session_end ms=6240')
    expect(within(skipped!).getByText('WARN')).toBeTruthy()
    expect(within(skipped!).getByText('Bedroom')).toBeTruthy()
    expect(skipped!.textContent).toContain('<img src=x onerror=alert(1)> track skipped truncated=true')
    expect(within(server!).getByText('server')).toBeTruthy()
    // text, never markup
    const list = screen.getByRole('region', { name: 'Stored lines' })
    expect(list.querySelector('b, img')).toBeNull()
  })

  it('asks the server for what the filter sets, the range in the household\'s zone', async () => {
    const { user, reads } = renderLogs()
    await screen.findByText('config_apply_failed code=<b>bad</b>')

    await user.selectOptions(screen.getByRole('combobox', { name: 'Speaker' }), 'Kitchen')
    await user.type(screen.getByRole('textbox', { name: 'Source' }), 'audiod')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Lowest level' }), 'Warn')
    await user.type(screen.getByRole('searchbox', { name: 'Contains' }), 'timeout')
    fireEvent.change(screen.getByLabelText('From'), {
      target: { value: '2026-11-12T21:00' },
    })
    fireEvent.change(screen.getByLabelText('Until'), {
      target: { value: '2026-11-12T22:00' },
    })

    await waitFor(() => expect(reads().at(-1)).toEqual({
      device: KITCHEN,
      source: 'audiod',
      level: 'warn',
      q: 'timeout',
      since: '2026-11-12T15:00:00.000Z',
      until: '2026-11-12T16:00:00.000Z',
      limit: '200',
    }))
    expect(screen.getByText('Times are Asia/Bishkek\'s.')).toBeTruthy()
  })

  it('says a search without From reads the last day', async () => {
    const { user } = renderLogs()
    await screen.findByText('config_apply_failed code=<b>bad</b>')
    expect(screen.queryByText(/reads the last day/)).toBeNull()
    await user.selectOptions(screen.getByRole('combobox', { name: 'Lowest level' }), 'Error')
    expect(screen.getByText('A search by text or level reads the last day unless From is set.')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('From'), {
      target: { value: '2026-11-10T21:00' },
    })
    expect(screen.queryByText(/reads the last day/)).toBeNull()
  })

  it('refuses a range that ends before it starts on its field, and asks nothing for it', async () => {
    const { reads } = renderLogs()
    await screen.findByText('config_apply_failed code=<b>bad</b>')
    fireEvent.change(screen.getByLabelText('From'), {
      target: { value: '2026-11-12T22:00' },
    })
    fireEvent.change(screen.getByLabelText('Until'), {
      target: { value: '2026-11-12T21:00' },
    })
    const until = screen.getByLabelText('Until')
    await waitFor(() => expect(until.getAttribute('aria-invalid')).toBe('true'))
    expect(screen.getByRole('alert').textContent).toBe('Until must be after From.')
    await new Promise(resolve => setTimeout(resolve, 400))
    expect(reads().some(q => q.until !== undefined)).toBe(false)
  })

  it('words the server\'s refusal of a filter on its field', async () => {
    const { user } = renderLogs(request => queryOf(request).q
      ? Response.json({ error: 'invalid', path: '/query/q' }, { status: 422 })
      : Response.json({ logs: STORED }))
    await screen.findByText('config_apply_failed code=<b>bad</b>')
    const text = screen.getByRole('searchbox', { name: 'Contains' })
    await user.type(text, 'x')
    await waitFor(() => expect(text.getAttribute('aria-invalid')).toBe('true'))
    expect(screen.getByText('The server refused this filter (invalid).')).toBeTruthy()
  })

  it('loads older lines from just after the oldest, keeping the ones that share its time', async () => {
    const at = (i: number) => new Date(Date.parse('2026-11-12T15:04:12Z') - i).toISOString()
    // a full page whose oldest two lines share one time
    const first = Array.from({ length: 200 }, (_, i) => stored({
      ts: at(Math.min(i, 198)),
      body: `line ${i}`,
    }))
    const boundary = first.at(-1)!.ts
    const older = [
      first.at(-1)!,
      stored({ ts: boundary, body: 'line 200, at the same time' }),
      ...Array.from({ length: 50 }, (_, i) => stored({
        ts: at(300 + i),
        body: `line ${201 + i}`,
      })),
    ]
    const { user, reads } = renderLogs(request => Response.json({
      logs: queryOf(request).until === justAfter(boundary)
        ? older
        : first,
    }))
    await screen.findByText('line 0')
    expect(shownCount('Stored lines')).toBe(200)

    await user.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(shownCount('Stored lines')).toBe(251))
    expect(reads()).toEqual([
      { limit: '200' },
      {
        limit: '200',
        until: justAfter(boundary),
      },
    ])
    // a page that came back short was the last
    expect(screen.queryByRole('button', { name: 'Load older' })).toBeNull()
  })

  it('holds at most 2,000 stored lines, ten pages however many each one repeats, and says how to read further back', async () => {
    let page = 0
    // each older page starts with the oldest line of the one before, which it leaves out
    const { user, reads } = renderLogs(() => {
      const start = page++ * 199
      return Response.json({
        logs: Array.from({ length: 200 }, (_, i) => stored({
          ts: new Date(Date.parse('2026-11-12T15:00:00Z') - (start + i) * 1000).toISOString(),
          body: `line ${start + i}`,
        })),
      })
    })
    await screen.findByText('line 0')
    for (let pages = 1; pages < 10; pages++) {
      await user.click(screen.getByRole('button', { name: 'Load older' }))
      await waitFor(() => expect(shownCount('Stored lines')).toBe(200 + pages * 199))
    }
    expect(reads()).toHaveLength(10)
    expect(screen.queryByRole('button', { name: 'Load older' })).toBeNull()
    expect(screen.getByText('The page holds 2,000 lines: set Until to read further back.')).toBeTruthy()
    // the button went with the last page it could load: the focus moves to the lines
    await expectFocusOn(screen.getByRole('region', { name: 'Stored lines' }))
  })

  it('reads only the newest page again on Read the newest', async () => {
    const at = (i: number) => new Date(Date.parse('2026-11-12T15:00:00Z') - i * 1000).toISOString()
    const first = Array.from({ length: 200 }, (_, i) => stored({
      ts: at(i),
      body: `line ${i}`,
    }))
    const { user, reads } = renderLogs(request => Response.json({
      logs: queryOf(request).until
        ? [
            stored({
              ts: at(500),
              body: 'older line',
            }),
          ]
        : first,
    }))
    await screen.findByText('line 0')
    await user.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(shownCount('Stored lines')).toBe(201))
    await user.click(screen.getByRole('button', { name: 'Read the newest' }))
    await waitFor(() => expect(reads()).toHaveLength(3))
    await waitFor(() => expect(shownCount('Stored lines')).toBe(200))
    expect(reads()[2]).toEqual({ limit: '200' })
    await new Promise(resolve => setTimeout(resolve, 200))
    expect(reads()).toHaveLength(3)

    // and so does turning the tail off
    await user.click(screen.getByRole('button', { name: 'Load older' }))
    await waitFor(() => expect(shownCount('Stored lines')).toBe(201))
    act(() => FakeSocket.last.open())
    await user.click(screen.getByRole('switch', { name: 'Live' }))
    await user.click(screen.getByRole('switch', { name: 'Live' }))
    await waitFor(() => expect(shownCount('Stored lines')).toBe(200))
    await new Promise(resolve => setTimeout(resolve, 200))
    expect(reads().slice(4)).toEqual([
      { limit: '200' },
    ])
  })

  it('marks the lines of the filter before as such while the new one is read', async () => {
    let release = () => {}
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    const { user } = renderLogs(async (request) => {
      if (queryOf(request).q)
        await held
      return Response.json({ logs: STORED })
    })
    await screen.findByText('config_apply_failed code=<b>bad</b>')
    expect(screen.getByRole('region', { name: 'Stored lines' }).getAttribute('aria-busy')).toBeNull()
    await user.type(screen.getByRole('searchbox', { name: 'Contains' }), 'tls')
    await waitFor(() => expect(screen.getByRole('region', { name: 'Stored lines' }).getAttribute('aria-busy')).toBe('true'))
    release()
    await waitFor(() => expect(screen.getByRole('region', { name: 'Stored lines' }).getAttribute('aria-busy')).toBeNull())
  })

  it('teaches what would be here when nothing matches', async () => {
    renderLogs(json({ logs: [] }))
    expect(await screen.findByText('No lines')).toBeTruthy()
  })
})

describe('the live tail', () => {
  async function goLive(app: ReturnType<typeof renderLogs>) {
    await screen.findByText('config_apply_failed code=<b>bad</b>')
    act(() => FakeSocket.last.open())
    await app.user.click(screen.getByRole('switch', { name: 'Live' }))
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: ['logs', 'presence'],
    }))
    return screen.getByRole('region', { name: 'Live lines' })
  }

  it('subscribes to the logs topic while Live is on, and lets go when it is turned off or the page is left', async () => {
    const app = renderLogs()
    await goLive(app)
    await app.user.click(screen.getByRole('switch', { name: 'Live' }))
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: ['presence'],
    }))
    expect(screen.getByRole('region', { name: 'Stored lines' })).toBeTruthy()

    await app.user.click(screen.getByRole('switch', { name: 'Live' }))
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: ['logs', 'presence'],
    }))
    await act(() => app.router.navigate({ to: '/' }))
    await waitFor(() => expect(FakeSocket.last.frames().at(-1)).toEqual({
      subscribe: ['presence'],
    }))
  })

  it('appends the lines that match the filter, as text, newest at the end, from when it started', async () => {
    const app = renderLogs()
    await screen.findByText('config_apply_failed code=<b>bad</b>')
    await app.user.selectOptions(screen.getByRole('combobox', { name: 'Speaker' }), 'Kitchen')
    await goLive(app)
    expect(screen.getByText('Waiting for new lines')).toBeTruthy()

    sendLine({ body: 'before a pause' })
    await app.user.click(screen.getByRole('switch', { name: 'Live' }))
    await app.user.click(screen.getByRole('switch', { name: 'Live' }))
    sendLine({ body: 'wake_hit score=0.91' })
    sendLine({ deviceId: BEDROOM, body: 'another speaker' })
    sendLine({ body: 'reply <b>bold</b>', severity: 17, ts: '2026-11-12T15:05:01.250Z' })

    await waitFor(() => expect(lines('Live lines')).toHaveLength(2))
    const [first, last] = lines('Live lines')
    expect(first!.textContent).toContain('wake_hit score=0.91')
    expect(within(last!).getByText('21:05:01.250')).toBeTruthy()
    expect(within(last!).getByText('ERROR')).toBeTruthy()
    expect(last!.textContent).toContain('reply <b>bold</b>')
    expect(screen.getByRole('region', { name: 'Live lines' }).querySelector('b')).toBeNull()
    expect(screen.queryByText('before a pause')).toBeNull()
    expect(screen.queryByText('another speaker')).toBeNull()
  })

  it('says where lines went missing: past the feed\'s rate, or while this tab fell behind', async () => {
    const app = renderLogs()
    await goLive(app)
    sendLine({ body: 'wake_hit' })
    act(() => FakeSocket.last.receive({
      type: 'log_line_dropped',
      data: { count: 312 },
    }))
    act(() => FakeSocket.last.receive({ type: 'dropped', count: 4 }))
    await waitFor(() => expect(lines('Live lines')).toHaveLength(3))
    expect(lines('Live lines').map(line => line.textContent)).toEqual([
      expect.stringContaining('wake_hit'),
      '312 lines were not sent: the live feed sends at most 50 a second.',
      'Lines may be missing here: this tab fell behind the live feed.',
    ])
  })

  it('pauses while the reader is scrolled up: the list stops growing, counts new lines, and jumps to the latest', async () => {
    const app = renderLogs()
    const view = await goLive(app)
    sendLines(0, 30)
    await waitFor(() => expect(shownCount('Live lines')).toBe(30))
    await waitFor(() => expect(view.scrollTop).toBe(30 * 20 - VIEWPORT_PX))
    expect(screen.getByRole('status', { name: 'Live tail' }).textContent).toBe('Following new lines.')

    scrollViewport(view, 100)
    await waitFor(() => expect(screen.getByRole('status', { name: 'Live tail' }).textContent).toBe('Paused while you read.'))
    sendLines(30, 31)
    expect(screen.getByText('1 new line')).toBeTruthy()
    sendLines(31, 2130)
    expect(shownCount('Live lines')).toBe(30)
    expect(screen.getByText('2,000 new lines')).toBeTruthy()

    await app.user.click(screen.getByRole('button', { name: 'Jump to the latest lines' }))
    await waitFor(() => expect(shownCount('Live lines')).toBe(2000))
    await expectFocusOn(view)
    expect(screen.getByRole('status', { name: 'Live tail' }).textContent).toBe('Following new lines.')
    expect(screen.queryByRole('button', { name: 'Jump to the latest lines' })).toBeNull()
  })

  it('goes on following once the reader scrolls back to the end', async () => {
    const app = renderLogs()
    const view = await goLive(app)
    sendLines(0, 30)
    await waitFor(() => expect(view.scrollTop).toBe(400))
    scrollViewport(view, 0)
    await waitFor(() => expect(screen.getByRole('status', { name: 'Live tail' }).textContent).toBe('Paused while you read.'))
    sendLine({ body: 'line 30' })
    scrollViewport(view, 400)
    await waitFor(() => expect(shownCount('Live lines')).toBe(31))
    expect(screen.getByRole('status', { name: 'Live tail' }).textContent).toBe('Following new lines.')
  })

  it('holds at most 2,000 lines', async () => {
    const app = renderLogs()
    await goLive(app)
    sendLines(0, 2100)
    await waitFor(() => expect(shownCount('Live lines')).toBe(2000))
  })
})
