import { describe, expect, it } from 'vitest'
import { leaderLine } from './group-words'

const NAMES = new Map([
  ['tv', 'Bedroom TV'],
  ['mini', 'Bedroom Station 2'],
])
const nameOf = (id: string) => NAMES.get(id) ?? id

describe('the leader line', () => {
  it.each([
    {
      leaderId: '',
      clockLeaderId: '',
      expected: 'None can lead now',
    },
    {
      leaderId: 'mini',
      clockLeaderId: 'mini',
      expected: 'Bedroom Station 2',
    },
    // an old server's view, or a recompute that has not named one yet
    {
      leaderId: 'mini',
      clockLeaderId: '',
      expected: 'Bedroom Station 2',
    },
    {
      leaderId: 'tv',
      clockLeaderId: 'mini',
      expected: 'Playing from Bedroom TV · clock: Bedroom Station 2',
    },
    {
      leaderId: 'gone',
      clockLeaderId: 'mini',
      expected: 'Playing from gone · clock: Bedroom Station 2',
    },
  ])('$leaderId on $clockLeaderId\'s clock', ({ expected, ...group }) => {
    expect(leaderLine(group, nameOf)).toBe(expected)
  })
})
