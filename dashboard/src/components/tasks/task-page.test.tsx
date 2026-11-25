// @vitest-environment jsdom
import type { InspectJson, TaskJson } from '@/api/gen'
import type { Question } from '@/lib/tasks'
import type { FakeRoute } from '@/test/fake-api'
import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { DEVICES, KITCHEN, queryOf } from '@/test/household'
import { stubColorScheme } from '@/test/media'
import { detail, task } from '@/test/tasks'
import { renderApp } from '@/test/test-app'
import { setupUser } from '@/test/typing'
import { TaskPage } from './task-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const WHICH_DAY: Question = {
  kind: 'clarify',
  text: 'Which day?',
  options: ['Friday', 'Saturday'],
  token: 'q3',
}
const DEPOSIT: Question = {
  kind: 'approval',
  text: 'Pay a 40 EUR deposit?',
  options: ['once', 'deny'],
  token: 'q7',
}

const BOOK = task({
  shortId: 't-3',
  title: 'Book a table for two',
  status: 'waiting_input',
  question: WHICH_DAY,
})
const PAY = task({
  shortId: 't-4',
  title: 'Reserve the hotel',
  status: 'waiting_input',
  question: DEPOSIT,
})
const NEWS = task({
  shortId: 't-2',
  title: 'Summarize the news',
  status: 'running',
  progress: 'reading 3 of 5',
})
const DONE = task({
  shortId: 't-1',
  title: 'Order the groceries',
  status: 'done',
  finishedAt: '2026-11-12T09:30:00Z',
})

function inspect(of: TaskJson, over: Partial<InspectJson> = {}): InspectJson {
  return {
    status: of.status,
    progress: of.progress,
    question: of.question,
    result: '',
    errorCode: '',
    ...over,
  }
}

// one task's routes: its detail (with history), its live view and its transcript
function taskRoutes(of: TaskJson, over: { detail?: Partial<ReturnType<typeof detail>>, inspect?: FakeRoute } = {}): Record<string, FakeRoute> {
  return {
    [`GET /tasks/${of.shortId}`]: json(detail(of, over.detail)),
    [`GET /tasks/${of.shortId}/inspect`]: over.inspect ?? ((request) => {
      return Response.json(queryOf(request).detail === 'transcript'
        ? inspect(of, { progress: 'found 3 restaurants', transcript: 'step 1: search the map\nstep 2: ask which day' })
        : inspect(of, { progress: 'found 3 restaurants' }))
    }),
  }
}

function renderTask(of: TaskJson, routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: `/tasks/${of.shortId}`,
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /devices': json(DEVICES),
      ...taskRoutes(of),
      ...routes,
    },
    pages: {
      '/tasks/$id': TaskPage,
      '/tasks': () => <h1>Tasks</h1>,
    },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

describe('a task', () => {
  it('shows its state, its question, its live view and its history', async () => {
    renderTask(BOOK, taskRoutes(BOOK, {
      detail: {
        events: [
          { seq: 1, kind: 'progress', at: '2026-11-12T09:00:00Z' },
          {
            seq: 2,
            kind: 'answered',
            at: '2026-11-12T09:02:00Z',
            decision: 'once',
            via: 'voice',
            device: KITCHEN,
            words: 'Yes, go ahead.',
          },
          { seq: 3, kind: 'waiting_input', at: '2026-11-12T09:04:00Z' },
        ],
        notices: [
          {
            eventSeq: 2,
            text: 'The table booking asks which day.',
            createdAt: '2026-11-12T09:04:00Z',
            deliveredAt: null,
          },
        ],
      },
    }))
    const heading = await screen.findByRole('heading', { level: 1, name: 'Book a table for two' })
    expect(heading).toBeTruthy()
    expect(screen.getByText('Waiting for an answer', { selector: '[data-tone] span' })).toBeTruthy()
    await waitFor(() => expect(screen.getByText(/hermes · t-3 · Kitchen/).textContent).toBe('hermes · t-3 · Kitchen · started 12 Nov, 15:00'))
    const question = screen.getByRole('region', { name: 'Question' })
    expect(within(question).getByText('Which day?')).toBeTruthy()
    const live = screen.getByRole('region', { name: 'Live view' })
    expect(await within(live).findByText('found 3 restaurants')).toBeTruthy()
    const history = screen.getByRole('list', { name: 'Task history' })
    expect(within(history).getAllByRole('listitem').map(li => li.textContent)).toEqual([
      expect.stringContaining('Waiting for an answer'),
      expect.stringMatching(/Answered: once via voice.*From Kitchen.*Yes, go ahead./),
      expect.stringContaining('Progress'),
    ])
    const notices = screen.getByRole('region', { name: 'Notices' })
    expect(within(notices).getByText('The table booking asks which day.')).toBeTruthy()
    expect(within(notices).getByText('Not said yet')).toBeTruthy()
  })

  it('shows the run\'s transcript, as text, when asked', async () => {
    const { server, user } = renderTask(BOOK)
    const live = await screen.findByRole('region', { name: 'Live view' })
    // the page's pill states the status; the view does not repeat it
    expect(within(live).queryByText('Status')).toBeNull()
    const toggle = await within(live).findByRole('button', { name: 'Show the transcript' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    await user.click(toggle)
    const transcript = await within(live).findByRole('figure', { name: 'Transcript' })
    expect(transcript.textContent).toContain('step 1: search the map\nstep 2: ask which day')
    expect(server.requests.some(r => queryOf(r).detail === 'transcript')).toBe(true)
    // it opened in place: the toggle stays, with the focus, to hide it again
    expect(document.activeElement).toBe(toggle)
    expect(toggle.textContent).toBe('Hide the transcript')
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    await user.click(toggle)
    expect(within(live).queryByRole('figure', { name: 'Transcript' })).toBeNull()
  })

  it('says when the live view is unavailable, and keeps the rest', async () => {
    renderTask(NEWS, taskRoutes(NEWS, {
      inspect: json({ error: 'kind_unavailable' }, 502),
    }))
    const live = await screen.findByRole('region', { name: 'Live view' })
    expect(await within(live).findByText('The live view is unavailable. The server answered 502 kind_unavailable.')).toBeTruthy()
    expect(screen.getByRole('heading', { level: 1, name: 'Summarize the news' })).toBeTruthy()
  })

  it('reads itself again on a task event', async () => {
    const { server } = renderTask(BOOK)
    await screen.findByRole('heading', { level: 1, name: 'Book a table for two' })
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toEqual([
      {
        subscribe: ['tasks', 'presence'],
      },
    ]))
    const before = server.calls('GET /tasks/t-3')
    act(() => FakeSocket.last.receive({
      type: 'task_event',
      data: { shortId: 't-3', status: 'running', seq: 3 },
    }))
    await waitFor(() => expect(server.calls('GET /tasks/t-3')).toBe(before + 1))
  })

  it('reads a shown transcript again only once the run ended, not on each step\'s event', async () => {
    const { server, user } = renderTask(BOOK)
    const live = await screen.findByRole('region', { name: 'Live view' })
    await user.click(await within(live).findByRole('button', { name: 'Show the transcript' }))
    await within(live).findByRole('figure', { name: 'Transcript' })
    act(() => FakeSocket.last.open())
    await waitFor(() => expect(FakeSocket.last.frames()).toEqual([
      {
        subscribe: ['tasks', 'presence'],
      },
    ]))
    const transcripts = () => server.requests.filter(r => queryOf(r).detail === 'transcript').length
    const views = () => server.requests.filter(r => new URL(r.url).pathname.endsWith('/inspect') && !queryOf(r).detail).length
    const [transcriptsBefore, viewsBefore] = [
      transcripts(),
      views(),
    ]

    act(() => FakeSocket.last.receive({
      type: 'task_event',
      data: {
        short_id: 't-3',
        kind: 'progress',
        status: 'running',
        seq: 4,
      },
    }))
    await waitFor(() => expect(views()).toBe(viewsBefore + 1))
    expect(transcripts()).toBe(transcriptsBefore)

    act(() => FakeSocket.last.receive({
      type: 'task_event',
      data: {
        short_id: 't-3',
        kind: 'done',
        status: 'done',
        seq: 5,
      },
    }))
    await waitFor(() => expect(transcripts()).toBe(transcriptsBefore + 1))
  })
})

describe('the actions by state', () => {
  it('answers a clarify question with the reply and the question\'s token; no approval is offered', async () => {
    const { server, user } = renderTask(BOOK, {
      'POST /tasks/t-3/reply': json({ ...BOOK, status: 'running', question: null }),
    })
    const question = await screen.findByRole('region', { name: 'Question' })
    expect(within(question).queryByRole('button', { name: 'Approve once' })).toBeNull()
    expect(within(question).queryByRole('button', { name: 'Deny' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Cancel the task' })).toBeTruthy()
    await user.click(within(question).getByRole('button', { name: 'Use “Friday”' }))
    const reply = within(question).getByRole('textbox', { name: 'Your reply' })
    expect((reply as HTMLTextAreaElement).value).toBe('Friday')
    await user.type(reply, ' at eight')
    await user.click(within(question).getByRole('button', { name: 'Send the reply' }))
    await waitFor(() => expect(server.calls('POST /tasks/t-3/reply')).toBe(1))
    const post = server.requests.find(r => r.method === 'POST')!
    expect(post.headers.get('X-Cadence-CSRF')).toBe('1')
    expect(await post.clone().json()).toEqual({ message: 'Friday at eight', token: 'q3' })
  })

  it('words a refused reply inside its form, keeping what was typed', async () => {
    const { user } = renderTask(BOOK, {
      'POST /tasks/t-3/reply': json({ error: 'stale' }, 409),
    })
    const question = await screen.findByRole('region', { name: 'Question' })
    const reply = within(question).getByRole('textbox', { name: 'Your reply' })
    await user.type(reply, 'Friday')
    await user.click(within(question).getByRole('button', { name: 'Send the reply' }))
    expect((await within(question).findByRole('alert')).textContent).toBe('The server answered 409 stale.')
    // the button that sent it keeps the focus: it is never disabled under it
    expect(document.activeElement).toBe(within(question).getByRole('button', { name: 'Send the reply' }))
    expect((reply as HTMLTextAreaElement).value).toBe('Friday')
  })

  it('holds an empty reply', async () => {
    const { server, user } = renderTask(BOOK)
    const question = await screen.findByRole('region', { name: 'Question' })
    await user.click(within(question).getByRole('button', { name: 'Send the reply' }))
    expect(within(question).getByRole('alert').textContent).toBe('Required.')
    expect(server.calls('POST /tasks/t-3/reply')).toBe(0)
  })

  it.each([
    [
      'Approve once',
      'Approve this once?',
      'Approve once',
      'once',
    ],
    [
      'Deny',
      'Deny this request?',
      'Deny the request',
      'deny',
    ],
  ])('answers an approval with %s once confirmed; no reply is offered', async (button, title, confirm, decision) => {
    const { server, user } = renderTask(PAY, {
      ...taskRoutes(PAY),
      'POST /tasks/t-4/approve': json({ ...PAY, status: 'running', question: null }),
    })
    const question = await screen.findByRole('region', { name: 'Question' })
    expect(within(question).getByText('Pay a 40 EUR deposit?')).toBeTruthy()
    expect(within(question).queryByRole('textbox', { name: 'Your reply' })).toBeNull()
    await user.click(within(question).getByRole('button', { name: button }))
    const dialog = await screen.findByRole('alertdialog', { name: title })
    expect(server.calls('POST /tasks/t-4/approve')).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: confirm }))
    await waitFor(() => expect(server.calls('POST /tasks/t-4/approve')).toBe(1))
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({ decision, token: 'q7' })
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: 'Reserve the hotel' })))
  })

  it('never answers a question the owner did not see: a new one read while the dialog is open blocks it', async () => {
    let asked: Question = DEPOSIT
    const { server, queryClient, user } = renderTask(PAY, {
      'GET /tasks/t-4': () => Response.json(detail({ ...PAY, question: asked })),
      'POST /tasks/t-4/approve': json({ ...PAY, status: 'running', question: null }),
    })
    const question = await screen.findByRole('region', { name: 'Question' })
    await user.click(within(question).getByRole('button', { name: 'Approve once' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Approve this once?' })

    // approval A was answered by voice and Hermes asks B; the refetch jumps straight to it
    asked = {
      kind: 'approval',
      text: 'Pay the full 400 EUR now?',
      options: ['once', 'deny'],
      token: 'q8',
    }
    await act(() => queryClient.invalidateQueries({
      queryKey: [
        { _id: 'getTask' },
      ],
    }))
    await waitFor(() => expect(within(question).getByText('Pay the full 400 EUR now?')).toBeTruthy())
    expect(within(dialog).getByText('Pay a 40 EUR deposit?')).toBeTruthy()
    expect(within(dialog).getByRole('status').textContent).toContain('Changed since you opened this')
    await user.click(within(dialog).getByRole('button', { name: 'Approve once' }))
    expect(server.calls('POST /tasks/t-4/approve')).toBe(0)
  })

  it('shows the server\'s stale answer to an approval inside its dialog', async () => {
    const { user } = renderTask(PAY, {
      ...taskRoutes(PAY),
      'POST /tasks/t-4/approve': json({ error: 'stale' }, 409),
    })
    const question = await screen.findByRole('region', { name: 'Question' })
    await user.click(within(question).getByRole('button', { name: 'Deny' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Deny this request?' })
    await user.click(within(dialog).getByRole('button', { name: 'Deny the request' }))
    expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 409 stale.')
  })

  it('drops a reply typed for a question that was replaced, and answers the new one with its own token', async () => {
    let asked: Question = WHICH_DAY
    const { server, queryClient, user } = renderTask(BOOK, {
      'GET /tasks/t-3': () => Response.json(detail({ ...BOOK, question: asked })),
      'POST /tasks/t-3/reply': json({ ...BOOK, status: 'running', question: null }),
    })
    const question = await screen.findByRole('region', { name: 'Question' })
    await user.type(within(question).getByRole('textbox', { name: 'Your reply' }), 'Friday')
    asked = {
      kind: 'clarify',
      text: 'For how many people?',
      options: [],
      token: 'q9',
    }
    await act(() => queryClient.invalidateQueries({
      queryKey: [
        { _id: 'getTask' },
      ],
    }))
    await waitFor(() => expect(within(question).getByText('For how many people?')).toBeTruthy())
    const reply = within(question).getByRole('textbox', { name: 'Your reply' }) as HTMLTextAreaElement
    expect(reply.value).toBe('')
    await user.type(reply, 'Two')
    await user.click(within(question).getByRole('button', { name: 'Send the reply' }))
    await waitFor(() => expect(server.calls('POST /tasks/t-3/reply')).toBe(1))
    expect(await server.requests.find(r => r.method === 'POST')!.clone().json()).toEqual({ message: 'Two', token: 'q9' })
  })

  it('cancels an active task once confirmed, then gives the focus to its title', async () => {
    let cancelled = false
    const { server, user } = renderTask(NEWS, {
      'GET /tasks/t-2': () => Response.json(detail(cancelled
        ? { ...NEWS, status: 'cancelled' }
        : NEWS)),
      'POST /tasks/t-2/cancel': () => {
        cancelled = true
        return Response.json({ ...NEWS, status: 'cancelled' })
      },
    })
    await screen.findByRole('heading', { level: 1, name: 'Summarize the news' })
    expect(screen.queryByRole('region', { name: 'Question' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Cancel the task' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Cancel this task?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel the task' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Cancel the task' })).toBeNull())
    expect(server.calls('POST /tasks/t-2/cancel')).toBe(1)
    expect(screen.getByText('Cancelled', { selector: '[data-tone] span' })).toBeTruthy()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: 'Summarize the news' })))
  })

  it('offers nothing on a finished task', async () => {
    renderTask(DONE, taskRoutes(DONE, {
      detail: { result: 'Ordered: milk, bread, apples.' },
    }))
    await screen.findByRole('heading', { level: 1, name: 'Order the groceries' })
    expect(screen.getByRole('region', { name: 'Result' }).textContent).toContain('Ordered: milk, bread, apples.')
    expect(screen.queryByRole('button', { name: 'Cancel the task' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Question' })).toBeNull()
  })
})

it('has no accessibility violations axe can see without layout', async () => {
  renderTask(BOOK)
  await screen.findByRole('heading', { level: 1, name: 'Book a table for two' })
  await screen.findByText('found 3 restaurants')
  const results = await axe.run(document.body, {
    rules: {
      'color-contrast': { enabled: false },
    },
  })
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})
