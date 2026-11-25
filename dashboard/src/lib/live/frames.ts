// one event of the feed; data may carry a transcript, a reply or tool data, so it is shown in the page only, kept in
// memory only and never logged (rule 8, M7b-D6)
export interface LiveEvent {
  type: string
  deviceId?: string
  sessionId?: string
  turnId?: string
  at?: string
  data?: unknown
}

export type ServerFrame
  = | { kind: 'subscribed' }
    | { kind: 'dropped' }
    | { kind: 'refused', error: string, detail?: string }
    | { kind: 'event', event: LiveEvent }

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value !== ''
    ? value
    : undefined
}

export function parseFrame(raw: unknown): ServerFrame | undefined {
  if (typeof raw !== 'string')
    return undefined
  let frame: unknown
  try {
    frame = JSON.parse(raw)
  }
  catch {
    return undefined
  }
  if (typeof frame !== 'object' || frame === null)
    return undefined
  const { type, error, detail, deviceId, sessionId, turnId, at, data } = frame as Record<string, unknown>
  if (typeof error === 'string') {
    return {
      kind: 'refused',
      error,
      ...(text(detail) && {
        detail: text(detail),
      }),
    }
  }
  if (type === 'subscribed')
    return { kind: 'subscribed' }
  if (type === 'dropped')
    return { kind: 'dropped' }
  if (typeof type !== 'string' || type === '')
    return undefined
  const event: LiveEvent = { type }
  if (text(deviceId))
    event.deviceId = text(deviceId)
  if (text(sessionId))
    event.sessionId = text(sessionId)
  if (text(turnId))
    event.turnId = text(turnId)
  if (text(at))
    event.at = text(at)
  if (data !== undefined)
    event.data = data
  return { kind: 'event', event }
}
