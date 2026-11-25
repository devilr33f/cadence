import type { LogView } from '@/api/gen'
import { act } from '@testing-library/react'
import { FakeSocket } from './fake-socket'
import { BEDROOM, KITCHEN } from './household'

// a stored line as list-logs answers it
export function stored(over: Partial<LogView> = {}): LogView {
  return {
    ts: '2026-11-12T15:04:12.250Z',
    observedTs: '2026-11-12T15:04:12.300Z',
    deviceId: KITCHEN,
    deviceName: 'Kitchen',
    board: 'sim',
    source: 'cadenced',
    severity: 9,
    body: 'session_end ms=6240',
    truncated: false,
    continued: false,
    ...over,
  }
}

export const STORED: LogView[] = [
  stored({ body: 'config_apply_failed code=<b>bad</b>', severity: 17, ts: '2026-11-12T15:04:13.517Z' }),
  stored(),
  stored({
    deviceId: BEDROOM,
    deviceName: 'Bedroom',
    board: 'yandex-pickle',
    source: 'soloist',
    severity: 13,
    body: '<img src=x onerror=alert(1)> track skipped',
    truncated: true,
    ts: '2026-11-12T15:04:11.004Z',
  }),
  stored({
    deviceId: undefined,
    deviceName: undefined,
    board: undefined,
    source: 'server',
    body: 'otlp_export_failed code=tls',
    ts: '2026-11-12T15:04:10Z',
  }),
]

interface LiveLine {
  deviceId?: string
  source?: string
  severity?: number
  body: string
  ts?: string
}

// a log_line event as the live feed sends it
function lineFrame({ deviceId = KITCHEN, source = 'cadenced', severity = 9, body, ts = '2026-11-12T15:05:00.125Z' }: LiveLine) {
  return {
    type: 'log_line',
    deviceId,
    at: ts,
    data: { deviceId, source, severity, ts, body },
  }
}

export function sendLine(line: LiveLine) {
  act(() => FakeSocket.last.receive(lineFrame(line)))
}

// lines `line <from>` up to `line <to - 1>`, in one go
export function sendLines(from: number, to: number) {
  act(() => {
    for (let i = from; i < to; i++)
      FakeSocket.last.receive(lineFrame({ body: `line ${i}` }))
  })
}

export const ROW_PX = 20
export const VIEWPORT_PX = 200

function rowsIn(viewport: Element): number {
  return Number(viewport.querySelector('li[aria-setsize]')?.getAttribute('aria-setsize') ?? 0)
}

// jsdom lays nothing out: a log viewport is 200px tall, each line 20px, its content as tall as its lines, and a scroll
// there moves it and says so, as a browser's would
export function stubLogLayout() {
  const restore: (() => void)[] = []
  const define = (name: 'clientHeight' | 'clientWidth' | 'scrollHeight' | 'offsetHeight', get: (this: HTMLElement) => unknown) => {
    const before = Object.getOwnPropertyDescriptor(HTMLElement.prototype, name)
    Object.defineProperty(HTMLElement.prototype, name, { configurable: true, get })
    restore.push(() => {
      if (before)
        Object.defineProperty(HTMLElement.prototype, name, before)
      else
        Reflect.deleteProperty(HTMLElement.prototype, name)
    })
  }
  define('clientHeight', function () {
    return this.hasAttribute('data-log-viewport')
      ? VIEWPORT_PX
      : 0
  })
  define('clientWidth', function () {
    return this.hasAttribute('data-log-viewport')
      ? 600
      : 0
  })
  define('scrollHeight', function () {
    return this.hasAttribute('data-log-viewport')
      ? rowsIn(this) * ROW_PX
      : 0
  })
  define('offsetHeight', function () {
    return this.hasAttribute('data-index')
      ? ROW_PX
      : 0
  })
  const scrollTo = HTMLElement.prototype.scrollTo
  HTMLElement.prototype.scrollTo = function (this: HTMLElement, options?: ScrollToOptions | number) {
    if (typeof options === 'object' && options.top !== undefined) {
      this.scrollTop = Math.max(0, Math.min(options.top, this.scrollHeight - this.clientHeight))
      this.dispatchEvent(new Event('scroll'))
    }
  } as typeof HTMLElement.prototype.scrollTo
  restore.push(() => {
    HTMLElement.prototype.scrollTo = scrollTo
  })
  return () => restore.reverse().forEach(undo => undo())
}

// the reader scrolls a viewport to a place
export function scrollViewport(viewport: HTMLElement, top: number) {
  act(() => {
    viewport.scrollTop = top
    viewport.dispatchEvent(new Event('scroll'))
  })
}
