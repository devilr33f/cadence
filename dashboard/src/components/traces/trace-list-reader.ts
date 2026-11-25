import type { Client } from '@/api/gen/client'
import type { LiveEvent } from '@/lib/live/frames'
import type { SpanSignal } from '@/lib/live/span-channel'
import type { TraceRow } from '@/lib/traces/book'
import type { SpanEvent } from '@/lib/traces/span-event'
import type { StreamRow } from '@/lib/traces/streams'
import { listTraces } from '@/api/gen'
import { TraceBook } from '@/lib/traces/book'
import { SINCE_PAGE, SINCE_PAGES, TRACES_PAGE } from '@/lib/traces/queries'
import { droppedCount, readSpanEvent } from '@/lib/traces/span-event'
import { StreamBook } from '@/lib/traces/streams'

// events are folded at once and shown at most this often
const FLUSH_MS = 250
// a summary read follows events by this much, and runs at most once in this window
const SETTLE_MS = 500
const SINCE_SPACING_MS = 1000

export interface TraceListState {
  status: 'loading' | 'ready' | 'error'
  error: unknown
  rows: readonly TraceRow[]
  streams: readonly StreamRow[]
  // span events the feed held back since the page opened (read again from the store)
  dropped: number
}

export const INITIAL_LIST: TraceListState = {
  status: 'loading',
  error: null,
  rows: [],
  streams: [],
  dropped: 0,
}

// one list read for a device filter: the newest page, then the feed's span events folded in place, and a read of what
// changed since the last one (`since`) shortly after a span starts or ends and after every gap of the feed. it lives
// until stop, and says what to show through onChange
export class TraceListReader {
  #api: Client
  #device: string
  #onChange: (state: TraceListState) => void
  #state: TraceListState = INITIAL_LIST
  #book = new TraceBook()
  #streams = new StreamBook()
  // events that came while the first page was read, folded once it lands
  #waiting: SpanEvent[] | null = []
  #stopped = false
  #flushTimer: ReturnType<typeof setTimeout> | undefined
  #sinceTimer: ReturnType<typeof setTimeout> | undefined
  #sinceRunning = false
  #sinceAgain = false
  #sinceAt = 0

  constructor(api: Client, device: string, onChange: (state: TraceListState) => void) {
    this.#api = api
    this.#device = device
    this.#onChange = onChange
  }

  start() {
    this.#onChange(this.#state)
    void this.#readNewest()
  }

  stop() {
    this.#stopped = true
    clearTimeout(this.#flushTimer)
    clearTimeout(this.#sinceTimer)
  }

  signal(signal: SpanSignal) {
    if (this.#stopped)
      return
    if (signal.kind === 'gap') {
      void this.#readSince()
      return
    }
    this.#event(signal.event)
  }

  #set(patch: Partial<TraceListState>) {
    this.#state = {
      ...this.#state,
      ...patch,
    }
    this.#onChange(this.#state)
  }

  #show() {
    this.#set({
      rows: this.#book.rows(),
      streams: this.#streams.rows(Date.now()),
    })
  }

  #scheduleFlush() {
    this.#flushTimer ??= setTimeout(() => {
      this.#flushTimer = undefined
      if (!this.#stopped)
        this.#show()
    }, FLUSH_MS)
  }

  #event(raw: LiveEvent) {
    const dropped = droppedCount(raw)
    if (dropped !== undefined) {
      this.#set({ dropped: this.#state.dropped + dropped })
      this.#scheduleSince()
      return
    }
    const event = readSpanEvent(raw)
    if (!event)
      return
    // another device's trace stays out of a narrowed list, unless one of its spans named the device
    const device = this.#device
    if (device !== '' && event.span.deviceId !== device && !this.#book.has(event.span.traceId))
      return
    if (this.#streams.span(event.span, Date.now()))
      this.#scheduleFlush()
    if (this.#waiting) {
      this.#waiting.push(event)
      return
    }
    if (this.#book.span(event))
      this.#scheduleFlush()
    // an update only moves a stream's counters; a start or an end may change the trace's summary
    if (event.type !== 'span_update')
      this.#scheduleSince()
  }

  async #readNewest() {
    try {
      const { data } = await listTraces({
        client: this.#api,
        query: {
          limit: TRACES_PAGE,
          ...(this.#device !== '' && { device: this.#device }),
        },
        throwOnError: true,
      })
      if (this.#stopped)
        return
      this.#book.page(data.traces ?? [], data.seq, true)
      const waiting = this.#waiting ?? []
      this.#waiting = null
      for (const event of waiting)
        this.#book.span(event)
      this.#set({
        status: 'ready',
        rows: this.#book.rows(),
        streams: this.#streams.rows(Date.now()),
      })
      if (waiting.length > 0 || this.#sinceAgain) {
        this.#sinceAgain = false
        this.#scheduleSince()
      }
    }
    catch (error) {
      if (!this.#stopped)
        this.#set({ status: 'error', error })
    }
  }

  // a read after events settle, spaced from the last
  #scheduleSince() {
    if (this.#sinceTimer !== undefined || this.#stopped)
      return
    const wait = Math.max(SETTLE_MS, this.#sinceAt + SINCE_SPACING_MS - Date.now())
    this.#sinceTimer = setTimeout(() => {
      this.#sinceTimer = undefined
      void this.#readSince()
    }, wait)
  }

  // what changed after the last read, page by page; a call while one runs (or the first page loads) runs once after it
  async #readSince() {
    if (this.#sinceRunning || this.#waiting) {
      this.#sinceAgain = true
      return
    }
    this.#sinceRunning = true
    this.#sinceAt = Date.now()
    try {
      for (let i = 0; i < SINCE_PAGES; i++) {
        const { data } = await listTraces({
          client: this.#api,
          query: {
            since: this.#book.readSeq,
            limit: SINCE_PAGE,
            ...(this.#device !== '' && { device: this.#device }),
          },
          throwOnError: true,
        })
        if (this.#stopped)
          return
        const traces = data.traces ?? []
        this.#book.page(traces, data.seq, false)
        if (traces.length < SINCE_PAGE)
          break
      }
      this.#scheduleFlush()
    }
    catch {
      // the next event or gap reads again
    }
    finally {
      this.#sinceRunning = false
      if (this.#sinceAgain) {
        this.#sinceAgain = false
        this.#scheduleSince()
      }
    }
  }
}
