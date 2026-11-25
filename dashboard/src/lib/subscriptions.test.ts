import { describe, expect, it } from 'vitest'
import { checkTopic } from '@/components/integrations/subscriptions/subscription-refusal'
import { CLARIFY, ERRORED, INBOX, STOPPED } from '@/test/subscriptions'
import { groupByTopic, inboxState, isOpenQuestion, isSubtree, parentPrefix, subtreePrefix } from './subscriptions'

describe('an inbox item\'s state', () => {
  it('is held while busy, waiting, unread or read', () => {
    expect(inboxState(STOPPED)).toBe('held')
    expect(inboxState({ ...STOPPED, heldBusy: false })).toBe('pending')
    expect(inboxState(CLARIFY)).toBe('unread')
    expect(inboxState(ERRORED)).toBe('read')
  })

  it('counts a question open only while the server says so and it has one', () => {
    expect(isOpenQuestion(CLARIFY)).toBe(true)
    expect(isOpenQuestion({ ...CLARIFY, open: false })).toBe(false)
    expect(isOpenQuestion(STOPPED)).toBe(false)
  })
})

it('groups the updates by topic, the newest topic first and each topic\'s newest first', () => {
  const groups = groupByTopic(INBOX)
  expect(groups.map(group => group.topic)).toEqual(['t3code:work:thread:th-2', 't3code:work:thread:th-1'])
  expect(groups[1]!.items.map(item => item.id)).toEqual([3, 1])
  expect(groups[0]!.title).toBe('Dashboard pages')
})

describe('browsing topics', () => {
  it('opens a subtree at the topic before its wildcard, and goes up no higher than the provider', () => {
    expect(isSubtree('t3code:work:project:p1:*')).toBe(true)
    expect(isSubtree('t3code:work:thread:t1')).toBe(false)
    expect(subtreePrefix('t3code:work:project:p1:*')).toBe('t3code:work:project:p1')
    expect(parentPrefix('t3code:work:project:p1')).toBe('t3code:work:project')
    expect(parentPrefix('t3code:work')).toBeUndefined()
  })

  it('refuses a topic the server would not parse, before asking', () => {
    expect(checkTopic('')).toBe('Required.')
    expect(checkTopic('t3code:work')).toMatch(/^A topic is/)
    expect(checkTopic('t3code::x')).toBe('No part of a topic may be empty.')
    expect(checkTopic('t3code:*:x')).toBe('The * may only be the last part.')
    expect(checkTopic('T3:work:*')).toMatch(/^The module and the provider/)
    expect(checkTopic('t3code:work:*')).toBeUndefined()
    expect(checkTopic('t3code:my.work:thread:a%3Ab')).toBeUndefined()
  })
})
