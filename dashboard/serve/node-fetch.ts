import type { IncomingMessage, ServerResponse } from 'node:http'
import type { ReadableStream as NodeReadableStream } from 'node:stream/web'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'

// a host header of the forms an origin takes (name, ipv4 or [ipv6], and a port), else a fixed one: the page's url
// only routes, nothing is fetched from it
const HOST = /^[\w.-]+(?::\d{1,5})?$|^\[[\d:a-f.]+\](?::\d{1,5})?$/i

// the page's request: the path and query as sent, every header (the cookie the ssr client forwards among them), and a
// signal that ends the render once the reader is gone. a HEAD renders as its GET, whose body node then leaves out
export function toRequest(req: IncomingMessage, signal: AbortSignal): Request {
  const host = req.headers.host !== undefined && HOST.test(req.headers.host)
    ? req.headers.host
    : 'localhost'
  const headers = new Headers()
  for (let i = 0; i + 1 < req.rawHeaders.length; i += 2)
    headers.append(req.rawHeaders[i]!, req.rawHeaders[i + 1]!)
  return new Request(`http://${host}${req.url ?? '/'}`, { method: 'GET', headers, signal })
}

export async function sendResponse(res: ServerResponse, response: Response, extra: Record<string, string>): Promise<void> {
  const headers: Record<string, string | string[]> = { ...extra }
  response.headers.forEach((value, name) => {
    if (name !== 'set-cookie')
      headers[name] = value
  })
  const cookies = response.headers.getSetCookie()
  if (cookies.length > 0)
    headers['set-cookie'] = cookies
  res.writeHead(response.status, headers)
  if (!response.body) {
    res.end()
    return
  }
  await pipeline(Readable.fromWeb(response.body as NodeReadableStream), res)
}
