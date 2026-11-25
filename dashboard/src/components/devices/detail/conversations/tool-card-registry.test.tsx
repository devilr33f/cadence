// @vitest-environment jsdom
import type { TurnMessage } from '@/api/gen'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { toolCallSummary, toolResultView } from './tool-card-registry'
import { TurnMessages } from './turn-messages'

afterEach(cleanup)

// the stored shapes as the server writes them (server/internal/agent/tools): a call's
// arguments and the envelope the model read
function call(name: string, args: unknown, id = 'call-1') {
  return { id, name, arguments: args }
}

function result(name: string, content: string, id = 'call-1', code?: string): TurnMessage {
  return {
    seq: 2,
    role: 'tool',
    content,
    toolCallId: id,
    toolCalls: [],
    ...(code !== undefined && { code }),
  }
}

describe('a youtube search in a stored turn', () => {
  const found = JSON.stringify({
    ok: true,
    data: {
      items: [
        {
          ref: 'yt:v:abc',
          kind: 'channel',
          title: 'Купли Барамидзе',
          subtitle: '@kuplinov',
          subscribers: 3900000,
          videoCount: 2400,
          artwork: 'AbCdEfGhIjKlMnOpQrStUv1',
        },
        {
          ref: 'yt:v:def',
          kind: 'video',
          title: 'YT Probably Stolen 9',
          durationS: 2760,
          views: 133000,
          published: '2026-11-17',
          publishedApprox: true,
          channel: { ref: 'yt:c:abc', title: 'Купли Барамидзе' },
        },
      ],
      next: 'c2',
    },
  })

  it('draws the call as its query line, not raw json', () => {
    render(
      <>
        {toolCallSummary({
          call: call('youtube_search', { query: 'ролик куплинова', uploaded: 'week' }),
        })}
      </>,
    )
    expect(screen.getByText('«ролик куплинова»')).toBeDefined()
    expect(screen.getByText('uploaded week')).toBeDefined()
    expect(screen.queryByRole('figure')).toBeNull()
    // the stored arguments stay one click away
    const raw = screen.getByText('Raw arguments').closest('details')!
    expect(raw.textContent).toContain('ролик куплинова')
  })

  it('draws the results as rows with their counts, the envelope behind a disclosure', () => {
    render(
      <>
        {toolResultView({
          name: 'youtube_search',
          message: result('youtube_search', found),
          args: { query: 'куплинов' },
        })}
      </>,
    )
    expect(screen.getByText('2 results')).toBeDefined()
    expect(screen.getByText('YT Probably Stolen 9')).toBeDefined()
    // the second line: the channel, compact counts, the length, the date
    expect(screen.getByText('Купли Барамидзе · 133K views · 46:00 · 2026-11-17')).toBeDefined()
    expect(screen.getByText('@kuplinov · 3.9M subscribers · 2.4K videos')).toBeDefined()
    const raw = screen.getByText('Raw JSON').closest('details')!
    expect(raw.textContent).toContain('yt:v:def')
  })

  it('falls back to the raw figure for a tool the cards do not know', () => {
    render(
      <>
        {toolResultView({
          name: 'device_volume',
          message: result('device_volume', '{"ok":true,"data":{"level":10}}'),
          args: {},
        })}
      </>,
    )
    expect(screen.queryByText('Raw JSON')).toBeNull()
  })
})

describe('a catalog play that failed', () => {
  it('names the code in destructive text with the ref it tried', () => {
    const failed = JSON.stringify({
      ok: false,
      error: { code: 'unknown_ref', message: 'refusing' },
    })
    render(
      <>
        {toolResultView({
          name: 'catalog_play',
          message: result('catalog_play', failed, 'call-1', 'unknown_ref'),
          args: { ref: 'yt:v:gone', episode: '5' },
        })}
      </>,
    )
    expect(screen.getByText('unknown_ref')).toBeDefined()
    expect(screen.getByText('yt:v:gone')).toBeDefined()
    expect(screen.getByText('episode 5')).toBeDefined()
  })

  it('says what a play started when it worked', () => {
    const ok = JSON.stringify({
      ok: true,
      data: { playing: true, episode: '5' },
      target: 'dev-9',
    })
    render(
      <>
        {toolResultView({
          name: 'catalog_play',
          message: result('catalog_play', ok),
          args: { ref: 'yt:v:abc', episode: '5' },
        })}
      </>,
    )
    expect(screen.getByText('Started on the screen')).toBeDefined()
    expect(screen.getByText('episode 5')).toBeDefined()
  })
})

describe('a screen state read', () => {
  it('sums up the page, the visible items and the player', () => {
    const state = JSON.stringify({
      ok: true,
      data: {
        route: 'title',
        page: {
          ref: 't:1',
          kind: 'series',
          title: 'Поднятие уровня',
          show: {
            names: { ru: 'Поднятие уровня в одиночку' },
            year: 2024,
          },
        },
        visible: [
          { ref: 'e:1', kind: 'episode', title: 'Episode 1' },
          { ref: 'e:2', kind: 'episode', title: 'Episode 2' },
        ],
        player: {
          item: { ref: 'e:1', kind: 'episode', title: 'Episode 1' },
          positionS: 640,
          tracks: [
            { id: 'a', kind: 'audio' },
            { id: 's', kind: 'subtitle', selected: true },
          ],
        },
      },
    })
    render(
      <>
        {toolResultView({
          name: 'screen_state',
          message: result('screen_state', state),
          args: {},
        })}
      </>,
    )
    expect(screen.getByText('title')).toBeDefined()
    // the show's russian name over the title, as the screen's cards do
    expect(screen.getByText('Page: Поднятие уровня в одиночку')).toBeDefined()
    expect(screen.getByText('Visible: Episode 1 · Episode 2')).toBeDefined()
    expect(screen.getByText('Playing: Episode 1 · 10:40 · 2 tracks')).toBeDefined()
  })
})

describe('the turn messages with cards and figures together', () => {
  it('keeps an unknown tool\'s call and result as raw figures, a known one\'s as cards', () => {
    const messages: TurnMessage[] = [
      {
        seq: 0,
        role: 'user',
        content: 'включи ролик',
        toolCalls: [],
      },
      {
        seq: 1,
        role: 'assistant',
        content: '',
        toolCalls: [
          call('device_volume', { level: 10 }, 'c-vol'),
          call('youtube_search', { query: 'куплинов' }, 'c-yt'),
        ],
      },
      {
        seq: 2,
        role: 'tool',
        content: '{"ok":true,"data":{"level":10}}',
        toolCallId: 'c-vol',
        toolCalls: [],
      },
      {
        seq: 3,
        role: 'tool',
        content: '{"ok":true,"data":{"items":[{"ref":"yt:v:def","kind":"video","title":"YT Probably Stolen 9","views":133000}]}}',
        toolCallId: 'c-yt',
        toolCalls: [],
      },
    ]
    render(<TurnMessages messages={messages} event={false} />)
    // the unknown tool keeps both its stored figures
    expect(screen.getByRole('figure', { name: 'Tool call device_volume' })).toBeDefined()
    expect(screen.getByRole('figure', { name: 'Result of device_volume' })).toBeDefined()
    // the known tool draws its card, without a raw figure
    expect(screen.queryByRole('figure', { name: 'Tool call youtube_search' })).toBeNull()
    expect(screen.queryByRole('figure', { name: 'Result of youtube_search' })).toBeNull()
    expect(screen.getByText('YT Probably Stolen 9')).toBeDefined()
  })
})
