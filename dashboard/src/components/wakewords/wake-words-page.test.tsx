// @vitest-environment jsdom
import type { WakeWordNegatives, WakeWordView } from '@/api/gen'
import type { FakeRoute } from '@/test/fake-api'
import { cleanup, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, signedIn } from '@/test/fake-api'
import { FakeSocket } from '@/test/fake-socket'
import { stubColorScheme } from '@/test/media'
import { renderApp } from '@/test/test-app'
import { enter, setupUser } from '@/test/typing'
import { WakeWordsPage } from './wake-words-page'

beforeEach(() => {
  FakeSocket.reset()
  vi.stubGlobal('WebSocket', FakeSocket)
  stubColorScheme(false)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const READY: WakeWordView = {
  id: '0b5c3b1e-7d0c-4c5e-9a43-3d2f0f6a8b11',
  word: 'Джарвис',
  source: 'trained',
  status: 'ready',
  sha256: 'a'.repeat(64),
  threshold: 0.85,
  takes: 12,
  recall: 0.92,
  falsePerHour: 0,
  evalHours: 1.2,
  modelBytes: 200000,
  datasetKept: true,
  createdAt: '2026-11-20T10:00:00Z',
}

const TRAINING: WakeWordView = {
  id: '1b5c3b1e-7d0c-4c5e-9a43-3d2f0f6a8b11',
  word: 'Пятница',
  source: 'trained',
  status: 'training',
  modelBytes: 0,
  datasetKept: false,
  progress: { stage: 'train', fraction: 0.4 },
  createdAt: '2026-11-20T11:00:00Z',
}

const NEGATIVES: WakeWordNegatives = {
  configured: true,
  present: false,
  bytes: 0,
  windows: 0,
  streams: 0,
}

function renderPage(routes: Record<string, FakeRoute> = {}) {
  const app = renderApp({
    path: '/wakewords',
    routes: {
      'GET /session': signedIn,
      'GET /settings/global': json({ timeZone: 'Asia/Bishkek' }),
      'GET /wakewords': json([TRAINING, READY]),
      'GET /wakewords/negatives': json(NEGATIVES),
      ...routes,
    },
    pages: { '/wakewords': WakeWordsPage },
  })
  return {
    ...app,
    user: setupUser(),
  }
}

describe('the wake words page', () => {
  it('lists each word with its status and a training\'s stage', async () => {
    renderPage()
    const table = await screen.findByRole('table', { name: 'Wake words' })
    await within(table).findByText('Джарвис')
    expect(within(table).getByText('Ready')).toBeTruthy()
    expect(within(table).getByText('Training')).toBeTruthy()
    expect(within(table).getByText('40%')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Retrain: Джарвис' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Retrain: Пятница' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Download the model: Джарвис' }).getAttribute('href'))
      .toBe(`/api/v1/wakewords/${READY.id}/model`)
    expect(screen.getByText('Not fetched')).toBeTruthy()
  })

  it('sends the dataset with its word and keep, and shows a dataset refusal in words', async () => {
    let query = ''
    const { user } = renderPage({
      'POST /wakewords/datasets': (req) => {
        query = new URL(req.url).search
        return Response.json({ status: 422, error: 'invalid_dataset', detail: 'no_ambient' }, { status: 422 })
      },
    })
    await screen.findByText('Джарвис')
    await user.click(screen.getByRole('button', { name: 'Train the word' }))
    expect(await screen.findByText('Name the word.')).toBeTruthy()
    await enter(user, screen.getAllByRole('textbox', { name: /^Word/ })[0]!, 'Компьютер')
    await user.upload(screen.getByLabelText(/^Dataset zip/), new File(['PK'], 'word.zip', { type: 'application/zip' }))
    await user.click(screen.getByRole('switch', { name: 'Keep the recordings for a retrain' }))
    await user.click(screen.getByRole('button', { name: 'Train the word' }))
    expect(await screen.findByText('The zip has no room recording of a second or more: name it ambient.wav.')).toBeTruthy()
    await waitFor(() => expect(query).toContain('keep=true'))
    expect(decodeURIComponent(query)).toContain('word=Компьютер')
  })
})
