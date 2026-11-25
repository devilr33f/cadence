import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'

export interface SubscriptionErrors {
  device?: string
  topic?: string
  events?: string
  form?: string
}

// the server's refusals of a subscription (deploy/server/README.md, Subscriptions), each worded with its code
export const TOPIC_CODES: Readonly<Record<string, string>> = {
  invalid_topic: 'Not a topic: <module>:<instance>:<path…>, with * only as the last part',
  unknown_module: 'No module of this name can be followed',
  unknown_instance: 'The module has no provider of this name',
  module_failed: 'The module could not list what it has',
}

const EVENTS_CODES: Readonly<Record<string, string>> = {
  unsupported_event: 'The module sends none of some of these events',
}

export function codeText(texts: Readonly<Record<string, string>>, error: ApiError): string {
  const text = texts[error.error]
  return text === undefined
    ? describeError(error)
    : `${text} (${error.error}).`
}

// a refusal lands on the field it names: the topic's codes on the topic, the events' on the events, an unknown device
// on the speaker; anything else is the form's, in codes
export function subscriptionRefusal(error: unknown): SubscriptionErrors {
  if (!(error instanceof ApiError)) {
    return {
      form: describeError(error),
    }
  }
  if (error.status === 422 && Object.hasOwn(TOPIC_CODES, error.error)) {
    return {
      topic: codeText(TOPIC_CODES, error),
    }
  }
  if (error.status === 422 && Object.hasOwn(EVENTS_CODES, error.error)) {
    return {
      events: codeText(EVENTS_CODES, error),
    }
  }
  if (error.status === 404)
    return { device: `This speaker is no longer enrolled (${error.error}).` }
  return {
    form: describeError(error),
  }
}

const MODULE = /^[a-z][a-z0-9_-]{0,63}$/
const INSTANCE = /^[A-Z0-9][\w.-]{0,63}$/i

// the server's own parse (server/internal/subscriptions/topic.go), so a topic it would refuse is said before asking
export function checkTopic(topic: string): string | undefined {
  if (topic === '')
    return 'Required.'
  if (topic.length > 512)
    return 'At most 512 characters.'
  const segments = topic.split(':')
  if (segments.length < 3 || segments.length > 16)
    return 'A topic is <module>:<instance>:<path…> or <module>:<instance>:*.'
  if (segments.includes(''))
    return 'No part of a topic may be empty.'
  if (segments.slice(0, -1).includes('*'))
    return 'The * may only be the last part.'
  if (!MODULE.test(segments[0]!) || !INSTANCE.test(segments[1]!))
    return 'The module and the provider are names: letters, digits, "-", "_" (and "." in the provider).'
  return undefined
}
