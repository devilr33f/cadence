import type { Buffer } from 'node:buffer'
import type { AddressInfo } from 'node:net'
import type { Fields, Level } from './log.ts'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { createServer, request as httpRequest } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createHandler } from './handler.ts'
import { loadStaticFiles } from './static-files.ts'

interface Line extends Fields {
  level: Level
  msg: string
}

let dir: string
let close: () => Promise<void>
let base: string
let lines: Line[]
let rendered: Request[]

async function serve(render: (request: Request) => Promise<Response>) {
  const files = await loadStaticFiles(join(dir, 'client'))
  const handler = createHandler({
    files,
    render: async (request) => {
      rendered.push(request)
      return render(request)
    },
    log: (level, msg, fields = {}) => {
      lines.push({ level, msg, ...fields })
    },
  })
  const server = createServer(handler)
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  close = () => new Promise((resolve) => {
    server.closeAllConnections()
    server.close(() => resolve())
  })
}

async function page() {
  return new Response('<html>page</html>', {
    status: 200,
    headers: { 'content-type': 'text/html' },
  })
}

// a request as written on the wire: fetch would resolve the dot segments a hostile client sends as they are
function raw(path: string, method = 'GET'): Promise<{ status: number, body: string }> {
  return new Promise((resolve, reject) => {
    const req = httpRequest(`${base}/`, { path, method }, (res) => {
      let body = ''
      res.on('data', (chunk: Buffer) => {
        body += chunk.toString()
      })
      res.on('end', () => resolve({ status: res.statusCode ?? 0, body }))
    })
    req.on('error', reject)
    req.end()
  })
}

// the line is written once the response is done, which the client may see first
async function logged(count: number): Promise<Line[]> {
  for (let i = 0; i < 50 && lines.length < count; i++)
    await new Promise(resolve => setTimeout(resolve, 10))
  return lines
}

beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'cadence-serve-'))
  await mkdir(join(dir, 'client', 'assets'), { recursive: true })
  await writeFile(join(dir, 'client', 'assets', 'app-0f1e2d.css'), 'body{color:red}')
  await writeFile(join(dir, 'client', 'favicon.svg'), '<svg/>')
  await writeFile(join(dir, 'outside.txt'), 'outside the build')
  lines = []
  rendered = []
})

afterEach(async () => {
  await close()
  await rm(dir, { recursive: true, force: true })
})

describe('the dashboard server', () => {
  it('serves a build file by its path, the hashed ones for good', async () => {
    await serve(page)
    const css = await fetch(`${base}/assets/app-0f1e2d.css`)
    expect(css.status).toBe(200)
    expect(css.headers.get('content-type')).toBe('text/css; charset=utf-8')
    expect(css.headers.get('cache-control')).toBe('public, max-age=31536000, immutable')
    expect(css.headers.get('x-content-type-options')).toBe('nosniff')
    expect(await css.text()).toBe('body{color:red}')
    const icon = await fetch(`${base}/favicon.svg`)
    expect(icon.headers.get('content-type')).toBe('image/svg+xml')
    expect(icon.headers.get('cache-control')).toBe('public, max-age=3600')
    expect(rendered).toHaveLength(0)
  })

  it.each([
    '/../outside.txt',
    '/assets/../../outside.txt',
    '/assets/%2e%2e/%2e%2e/outside.txt',
    '/assets/..%2f..%2foutside.txt',
    '/assets\\..\\..\\outside.txt',
  ])('never reads a file outside the build: %s is a page like any other', async (path) => {
    await serve(page)
    const answer = await raw(path)
    expect(answer.body).toBe('<html>page</html>')
    expect(rendered).toHaveLength(1)
  })

  it('renders any other path with its query and cookie, and logs the path and status alone', async () => {
    await serve(page)
    const answer = await fetch(`${base}/devices/d1?redirect=%2Ftasks&token=cdn_s3cret`, {
      headers: { cookie: 'cadence_session=c00kie-value' },
    })
    expect(answer.status).toBe(200)
    expect(await answer.text()).toBe('<html>page</html>')
    expect(answer.headers.get('x-frame-options')).toBe('DENY')
    const request = rendered[0]!
    expect(new URL(request.url).pathname).toBe('/devices/d1')
    expect(new URL(request.url).search).toBe('?redirect=%2Ftasks&token=cdn_s3cret')
    expect(request.headers.get('cookie')).toBe('cadence_session=c00kie-value')
    const [line] = await logged(1)
    expect(line).toMatchObject({
      level: 'INFO',
      msg: 'request',
      method: 'GET',
      path: '/devices/d1',
      status: 200,
    })
    expect(Object.keys(line!).sort()).toEqual([
      'level',
      'method',
      'ms',
      'msg',
      'path',
      'status',
    ])
    const text = JSON.stringify(lines)
    for (const secret of [
      'cdn_s3cret',
      'c00kie-value',
      'redirect',
      'cadence_session',
    ])
      expect(text).not.toContain(secret)
  })

  it('keeps each cookie a page sets as its own header', async () => {
    await serve(async () => {
      const headers = new Headers({ 'content-type': 'text/html' })
      headers.append('set-cookie', 'a=1; Path=/')
      headers.append('set-cookie', 'b=2; Path=/')
      return new Response('ok', { headers })
    })
    const answer = await fetch(`${base}/`)
    expect(answer.headers.getSetCookie()).toEqual(['a=1; Path=/', 'b=2; Path=/'])
  })

  it.each(['POST', 'PUT', 'DELETE', 'OPTIONS'])('refuses %s: it serves pages and their files only', async (method) => {
    await serve(page)
    const answer = await fetch(`${base}/login`, { method })
    expect(answer.status).toBe(405)
    expect(answer.headers.get('allow')).toBe('GET, HEAD')
    expect(rendered).toHaveLength(0)
    const [line] = await logged(1)
    expect(line).toMatchObject({
      msg: 'request',
      method,
      path: '/login',
      status: 405,
    })
  })

  // the login form's submit before hydration (method="post" action="/login")
  it('refuses a form\'s post without echoing or logging what it carries', async () => {
    await serve(page)
    const secret = 'cadence-serve-test-password'
    const answer = await fetch(`${base}/login?redirect=%2Fdevices`, {
      method: 'POST',
      headers: { 'content-type': 'application/x-www-form-urlencoded' },
      body: `username=owner&password=${secret}`,
    })
    expect(answer.status).toBe(405)
    expect(await answer.text()).toBe('Method Not Allowed')
    const [line] = await logged(1)
    expect(line).toMatchObject({ method: 'POST', path: '/login', status: 405 })
    expect(JSON.stringify(lines)).not.toContain(secret)
    expect(JSON.stringify(lines)).not.toContain('redirect')
  })

  it('renders a HEAD as the GET it asks the headers of, and sends no body', async () => {
    await serve(page)
    const answer = await fetch(`${base}/devices`, { method: 'HEAD' })
    expect(answer.status).toBe(200)
    expect(await answer.text()).toBe('')
    expect(rendered[0]?.method).toBe('GET')
  })

  it('answers its health without logging it', async () => {
    await serve(page)
    const answer = await fetch(`${base}/healthz`)
    expect(answer.status).toBe(200)
    expect(await answer.json()).toEqual({ status: 'ok' })
    await new Promise(resolve => setTimeout(resolve, 30))
    expect(lines).toEqual([])
    expect(rendered).toHaveLength(0)
  })

  it('answers a page that throws with a bare 500, naming only the error\'s class', async () => {
    await serve(async () => {
      throw new TypeError('the owner\'s transcript')
    })
    const answer = await fetch(`${base}/tasks`)
    expect(answer.status).toBe(500)
    expect(await answer.text()).toBe('Internal Server Error')
    await logged(2)
    expect(lines).toContainEqual({ level: 'ERROR', msg: 'render_failed', error: 'TypeError' })
    expect(lines).toContainEqual(expect.objectContaining({ msg: 'request', path: '/tasks', status: 500 }))
    expect(JSON.stringify(lines)).not.toContain('transcript')
  })

  it('refuses a target that is no path', async () => {
    await serve(page)
    const answer = await raw('http://evil.example/devices')
    expect(answer.status).toBe(400)
    expect(rendered).toHaveLength(0)
  })

  it('stops a page\'s render when its reader goes away', async () => {
    let aborted!: () => void
    const gone = new Promise<void>((resolve) => {
      aborted = resolve
    })
    await serve(async (request) => {
      request.signal.addEventListener('abort', () => aborted())
      return new Response(new ReadableStream({
        start(controller) {
          controller.enqueue(new TextEncoder().encode('<html>'))
        },
      }))
    })
    const reader = new AbortController()
    const answer = await fetch(`${base}/`, { signal: reader.signal })
    expect(answer.status).toBe(200)
    reader.abort()
    await expect(gone).resolves.toBeUndefined()
  })
})
