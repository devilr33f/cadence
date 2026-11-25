// @vitest-environment jsdom
import type { TurnMessage } from '@/api/gen'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { TurnMessages } from './turn-messages'

afterEach(cleanup)

describe('a loaded skill in a stored turn', () => {
  it('is a compact line, with the full instructions result behind a disclosure', () => {
    const messages: TurnMessage[] = [
      {
        seq: 0,
        role: 'user',
        content: 'Play some music',
        toolCalls: [],
      },
      {
        seq: 1,
        role: 'assistant',
        content: '',
        toolCalls: [
          {
            id: 'skill-1',
            name: 'load_skill',
            arguments: { name: 'music' },
          },
        ],
      },
      {
        seq: 2,
        role: 'tool',
        content: '{"name":"music","instructions":"never invent an id; the full instructions stay here"}',
        toolCallId: 'skill-1',
        toolCalls: [],
      },
    ]
    render(<TurnMessages messages={messages} event={false} />)

    expect(screen.getByText('Loaded skill:', { exact: false }).textContent).toBe('Loaded skill: music')
    expect(screen.queryByRole('figure', { name: 'Tool call load_skill' })).toBeNull()
    const disclosure = screen.getByText('Raw skill result').closest('details')!
    expect(disclosure.open).toBe(false)
    expect(disclosure.textContent).toContain('the full instructions stay here')
  })
})
