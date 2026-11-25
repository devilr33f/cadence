import type { LiveEvent } from './frames'
import type { Topic } from './topics'
import { backoffDelay } from './backoff'
import { parseFrame } from './frames'
import { sameTopics, unionTopics } from './topics'

export type FeedStatus = 'connecting' | 'live' | 'reconnecting' | 'closed'

// why events may have been missed: the feed reconnected, the server dropped some for a slow client, or a topic joined a
// live subscription (its events until then went undelivered)
export type ResyncCause = 'reconnect' | 'dropped' | 'joined'

const OPEN = 1
const NORMAL_CLOSE = 1000
// the server's policy close: an ended credential (unauthorized) or a client frame past its limits
const POLICY_CLOSE = 1008

// the server closes a feed past 10 client frames a second, its pings' pongs counted: a subscription goes out at most
// every 200 ms (5 a second), and the topics are a closed set, so a frame stays far under its 4 KiB
export const SUBSCRIBE_SPACING_MS = 200

// a connection that stayed open this long counts as served: its drop starts the backoff over at 1 s
export const HEALTHY_MS = 10_000

export interface SocketLike {
  readonly readyState: number
  send: (data: string) => void
  close: (code?: number) => void
  onopen: ((event: Event) => void) | null
  onmessage: ((event: MessageEvent) => void) | null
  onclose: ((event: CloseEvent) => void) | null
}

export interface LiveFeedOptions {
  url: () => string
  socket?: new (url: string) => SocketLike
  random?: () => number
  onEvent: (event: LiveEvent) => void
  // events may have been missed (a reconnect, a dropped count, a topic that joined): what those topics feed must be
  // fetched again
  onResync: (topics: readonly Topic[], cause: ResyncCause) => void
  onStatus: (status: FeedStatus) => void
  // whether the session still holds, asked after a 1008 or an upgrade that never opened (a refused upgrade reaches the
  // page as a bare 1006)
  checkSession: () => Promise<boolean>
  onSignedOut: () => void
  // the server refused a subscription: codes only, for the developer
  onRefused?: (error: string, detail?: string) => void
}

// one websocket per tab to the live feed: pages say which topics they want, the feed subscribes to their union, and
// reconnects with backoff until the session ends
export class LiveFeed {
  readonly #options: LiveFeedOptions
  readonly #wanted = new Map<symbol, readonly Topic[]>()
  #updateQueued = false
  #socket: SocketLike | undefined
  #sent: readonly Topic[] | undefined
  #sentAt = Number.NEGATIVE_INFINITY
  #subscribeTimer: ReturnType<typeof setTimeout> | undefined
  #retryTimer: ReturnType<typeof setTimeout> | undefined
  #attempt = 0
  #reconnecting = false
  #running = false
  // bumped by every start and stop, so a session check still in flight from before is ignored
  #generation = 0
  #status: FeedStatus | undefined

  constructor(options: LiveFeedOptions) {
    this.#options = options
  }

  // a page's topics, until the returned release
  want(topics: readonly Topic[]): () => void {
    const token = Symbol('page')
    this.#wanted.set(token, topics)
    this.#queueUpdate()
    return () => {
      this.#wanted.delete(token)
      this.#queueUpdate()
    }
  }

  // again after a stop too: react's strict mode mounts, unmounts and mounts the provider once in development
  start() {
    if (this.#running)
      return
    this.#running = true
    this.#generation++
    this.#connect()
  }

  stop() {
    this.#running = false
    this.#generation++
    clearTimeout(this.#subscribeTimer)
    clearTimeout(this.#retryTimer)
    this.#subscribeTimer = undefined
    this.#retryTimer = undefined
    const socket = this.#socket
    this.#socket = undefined
    if (socket) {
      socket.onopen = null
      socket.onmessage = null
      socket.onclose = null
      socket.close(NORMAL_CLOSE)
    }
  }

  #topics(): Topic[] {
    return unionTopics(this.#wanted.values())
  }

  // a page swap releases the old page's topics and wants the new one's in one commit: the union is looked at once the
  // tick is over, so the two cancel out instead of sending a brief subscription without them (its events lost)
  #queueUpdate() {
    if (this.#updateQueued)
      return
    this.#updateQueued = true
    queueMicrotask(() => {
      this.#updateQueued = false
      this.#scheduleSubscribe()
    })
  }

  #setStatus(status: FeedStatus) {
    if (status === this.#status)
      return
    this.#status = status
    this.#options.onStatus(status)
  }

  #connect() {
    this.#retryTimer = undefined
    if (!this.#running)
      return
    this.#setStatus('connecting')
    const Socket = this.#options.socket ?? globalThis.WebSocket
    let socket: SocketLike
    try {
      socket = new Socket(this.#options.url())
    }
    catch {
      this.#retry()
      return
    }
    this.#socket = socket
    let openedAt: number | undefined
    socket.onopen = () => {
      openedAt = Date.now()
      // a new connection: the server has seen none of its frames yet
      this.#sent = undefined
      this.#sentAt = Number.NEGATIVE_INFINITY
      this.#setStatus('live')
      this.#sendSubscribe()
      if (this.#reconnecting)
        this.#options.onResync(this.#topics(), 'reconnect')
      this.#reconnecting = true
    }
    socket.onmessage = message => this.#receive(message.data)
    socket.onclose = ({ code }) => {
      if (this.#socket !== socket)
        return
      this.#socket = undefined
      clearTimeout(this.#subscribeTimer)
      this.#subscribeTimer = undefined
      // a feed that held a while starts the backoff over; one that drops at once (a server that accepts and closes)
      // keeps backing off
      if (openedAt !== undefined && Date.now() - openedAt >= HEALTHY_MS)
        this.#attempt = 0
      if (code === POLICY_CLOSE || openedAt === undefined)
        this.#recheck()
      else
        this.#retry()
    }
  }

  #receive(raw: unknown) {
    const frame = parseFrame(raw)
    switch (frame?.kind) {
      case 'dropped':
        this.#options.onResync(this.#topics(), 'dropped')
        break
      case 'refused':
        this.#options.onRefused?.(frame.error, frame.detail)
        break
      case 'event':
        this.#options.onEvent(frame.event)
        break
    }
  }

  #recheck() {
    const generation = this.#generation
    this.#setStatus('reconnecting')
    this.#options.checkSession().then((signedIn) => {
      if (generation !== this.#generation)
        return
      if (signedIn) {
        this.#retry()
        return
      }
      this.stop()
      this.#setStatus('closed')
      this.#options.onSignedOut()
    }, () => {
      if (generation === this.#generation)
        this.#retry()
    })
  }

  #retry() {
    if (!this.#running)
      return
    const delay = backoffDelay(this.#attempt, this.#options.random ?? Math.random)
    this.#attempt++
    this.#setStatus('reconnecting')
    this.#retryTimer = setTimeout(() => this.#connect(), delay)
  }

  #scheduleSubscribe() {
    if (this.#socket?.readyState !== OPEN || this.#subscribeTimer !== undefined || sameTopics(this.#sent, this.#topics()))
      return
    const wait = this.#sentAt + SUBSCRIBE_SPACING_MS - Date.now()
    if (wait <= 0) {
      this.#sendSubscribe()
      return
    }
    this.#subscribeTimer = setTimeout(() => {
      this.#subscribeTimer = undefined
      this.#sendSubscribe()
    }, wait)
  }

  #sendSubscribe() {
    const socket = this.#socket
    const topics = this.#topics()
    if (socket?.readyState !== OPEN || sameTopics(this.#sent, topics))
      return
    socket.send(JSON.stringify({ subscribe: topics }))
    // a topic joining a live subscription had its events until now go undelivered; a new connection's first frame
    // joins none (the reconnect's resync, or the pages' own first reads, cover it)
    const before = this.#sent
    const joined = before === undefined
      ? []
      : topics.filter(topic => !before.includes(topic))
    this.#sent = topics
    this.#sentAt = Date.now()
    if (joined.length > 0)
      this.#options.onResync(joined, 'joined')
  }
}
