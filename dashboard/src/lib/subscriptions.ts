import type { CreateSubscriptionRequest, InboxItem, Subscription, SubscriptionReplyRequest } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { createSubscription, deleteSubscription, replyToSubscription } from '@/api/gen'
import { getSubscriptionInboxOptions, listSubscriptionsOptions, listSubscriptionTopicsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export type SubscriptionEvent = NonNullable<CreateSubscriptionRequest['events']>[number]
export type Delivery = Subscription['delivery']
export type Until = Subscription['until']
export type Decision = NonNullable<SubscriptionReplyRequest['decision']>

// the shared event names, in the server's order; a module emits some of them and refuses the rest (unsupported_event)
export const EVENTS: readonly SubscriptionEvent[] = [
  'stopped',
  'question',
  'approval',
  'error',
  'done',
  'changed',
]

export const EVENT_TEXT: Record<SubscriptionEvent, string> = {
  stopped: 'Stopped',
  question: 'Question',
  approval: 'Approval',
  error: 'Error',
  done: 'Done',
  changed: 'Changed',
}

export const DELIVERY_TEXT: Record<Delivery, string> = {
  speaker: 'Spoken',
  agent: 'Agent turn',
  mute: 'Muted',
}

export const UNTIL_TEXT: Record<Until, string> = {
  unsubscribed: 'Until removed',
  next_stop: 'Until the next stop',
}

export const CREATED_BY_TEXT: Record<Subscription['createdBy'], string> = {
  voice: 'Voice',
  api: 'Dashboard or API',
}

// a topic's subtree: `<…>:*`. browsing it lists what lies under the topic before the wildcard
export const WILDCARD = ':*'

export function isSubtree(topic: string): boolean {
  return topic.endsWith(WILDCARD)
}

export function subtreePrefix(topic: string): string {
  return isSubtree(topic)
    ? topic.slice(0, -WILDCARD.length)
    : topic
}

// the parent of a browsed prefix, never above `<module>:<instance>`; undefined at the top
export function parentPrefix(prefix: string): string | undefined {
  const segments = prefix.split(':')
  return segments.length > 2
    ? segments.slice(0, -1).join(':')
    : undefined
}

export function subscriptionsQueryOptions(api: Client) {
  return sharedKey(listSubscriptionsOptions({ client: api }))
}

// a module's list under a prefix: titles are the outside system's own, so they live in this cache only
export function topicsQueryOptions(api: Client, prefix: string) {
  return {
    ...sharedKey(listSubscriptionTopicsOptions({
      client: api,
      query: { prefix },
    })),
    retry: false,
  }
}

export function inboxQueryOptions(api: Client, device: string) {
  return sharedKey(getSubscriptionInboxOptions({
    client: api,
    query: { device },
  }))
}

// an inbox item's state, as the page words it
export type InboxState = 'held' | 'pending' | 'unread' | 'read'

export function inboxState(item: Pick<InboxItem, 'state' | 'seen' | 'heldBusy'>): InboxState {
  if (item.state === 'pending')
    return item.heldBusy ? 'held' : 'pending'
  return item.seen ? 'read' : 'unread'
}

export function isOpenQuestion(item: InboxItem): item is InboxItem & { question: NonNullable<InboxItem['question']> } {
  return item.open === true && item.question !== undefined
}

export interface TopicGroup {
  topic: string
  // the newest item's title, the thread's name as the module last gave it
  title?: string
  items: InboxItem[]
}

// the updates by topic, the topic with the newest update first, each topic's newest first
export function groupByTopic(items: readonly InboxItem[]): TopicGroup[] {
  const groups = new Map<string, InboxItem[]>()
  for (const item of items) {
    const list = groups.get(item.topic)
    if (list)
      list.push(item)
    else
      groups.set(item.topic, [item])
  }
  const newest = (list: InboxItem[]) => list.reduce((at, item) => item.at > at
    ? item.at
    : at, '')
  return [
    ...groups.entries(),
  ]
    .map(([topic, list]) => {
      const sorted = [...list].sort((a, b) => b.at.localeCompare(a.at) || b.id - a.id)
      return { topic, title: sorted.find(item => item.title)?.title, items: sorted }
    })
    .sort((a, b) => newest(b.items).localeCompare(newest(a.items)))
}

// every query a subscription or an answer may change; the subscriptions topic refreshes them too
const SUBSCRIPTION_KEYS = [
  [
    { _id: 'listSubscriptions' },
  ],
  [
    { _id: 'getSubscriptionInbox' },
  ],
]

// called directly, not as mutations: an answer's words stay out of the mutation cache (rule 8)
export function useSubscriptionActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  async function refreshing<T>(action: () => Promise<T>): Promise<T> {
    try {
      return await action()
    }
    finally {
      for (const queryKey of SUBSCRIPTION_KEYS)
        void queryClient.invalidateQueries({ queryKey })
    }
  }
  return {
    create: (body: CreateSubscriptionRequest) => refreshing(async () => (await createSubscription({
      client: api,
      body,
      throwOnError: true,
    })).data),
    remove: (id: string) => refreshing(() => deleteSubscription({
      client: api,
      path: { id },
      throwOnError: true,
    })),
    reply: (body: SubscriptionReplyRequest) => refreshing(() => replyToSubscription({
      client: api,
      body,
      throwOnError: true,
    })),
  }
}
