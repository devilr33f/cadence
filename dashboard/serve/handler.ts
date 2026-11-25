import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Log } from './log.ts'
import type { StaticFile } from './static-files.ts'
import { createReadStream } from 'node:fs'
import { pipeline } from 'node:stream/promises'
import { errorClass } from './log.ts'
import { sendResponse, toRequest } from './node-fetch.ts'

export const HEALTH_PATH = '/healthz'

const READ = new Set(['GET', 'HEAD'])
// a request's path as logged: bounded, without its query (a redirect target, anything a link carried)
const PATH_LIMIT = 256

// on every answer: the dashboard is never framed, sniffed or named in another site's referer
const SECURITY_HEADERS: Record<string, string> = {
  'x-content-type-options': 'nosniff',
  'x-frame-options': 'DENY',
  'referrer-policy': 'same-origin',
}

export interface HandlerOptions {
  // the client build's files by url path (loadStaticFiles)
  files: ReadonlyMap<string, StaticFile>
  // start's server entry: a page for a request
  render: (request: Request) => Promise<Response>
  log: Log
}

function pathOf(target: string): string {
  const end = target.search(/[?#]/)
  return (end === -1
    ? target
    : target.slice(0, end)).slice(0, PATH_LIMIT)
}

function plain(res: ServerResponse, status: number, body: string, headers: Record<string, string> = {}) {
  res.writeHead(status, {
    ...SECURITY_HEADERS,
    'content-type': 'text/plain; charset=utf-8',
    ...headers,
  })
  res.end(body)
}

// the node server around start's fetch handler: its health, the client build's files, then a page for any other GET or
// HEAD. one line per request: its method, path, status and time, never its query, headers or body (M7b-D6)
export function createHandler({ files, render, log }: HandlerOptions) {
  return (req: IncomingMessage, res: ServerResponse) => {
    const started = performance.now()
    const method = req.method ?? 'GET'
    const target = req.url ?? '/'
    const path = target.startsWith('/')
      ? pathOf(target)
      : '-'
    if (path === HEALTH_PATH && READ.has(method)) {
      res.writeHead(200, { ...SECURITY_HEADERS, 'content-type': 'application/json', 'cache-control': 'no-store' })
      res.end(JSON.stringify({ status: 'ok' }))
      return
    }
    const reader = new AbortController()
    res.once('close', () => {
      reader.abort()
      log('INFO', 'request', {
        method,
        path,
        status: res.statusCode,
        ms: Math.round(performance.now() - started),
      })
    })
    void answer(req, res, method, target, path, reader.signal).catch(() => {
      // the reader went away mid-answer, or the stream broke: nothing left to send
      if (!res.headersSent)
        plain(res, 500, 'Internal Server Error')
      else
        res.destroy()
    })
  }

  async function answer(req: IncomingMessage, res: ServerResponse, method: string, target: string, path: string, signal: AbortSignal) {
    if (!target.startsWith('/')) {
      plain(res, 400, 'Bad Request')
      return
    }
    if (!READ.has(method)) {
      plain(res, 405, 'Method Not Allowed', { allow: 'GET, HEAD' })
      return
    }
    const file = files.get(path)
    if (file && path === target) {
      res.writeHead(200, {
        ...SECURITY_HEADERS,
        'content-type': file.type,
        'content-length': String(file.size),
        'cache-control': file.cacheControl,
      })
      if (method === 'HEAD')
        res.end()
      else
        await pipeline(createReadStream(file.file), res)
      return
    }
    let response: Response
    try {
      response = await render(toRequest(req, signal))
    }
    catch (error) {
      log('ERROR', 'render_failed', {
        error: errorClass(error),
      })
      plain(res, 500, 'Internal Server Error')
      return
    }
    await sendResponse(res, response, SECURITY_HEADERS)
  }
}
