// the dashboard's ssr server in its image (deploy/dashboard/Dockerfile): `node serve/main.ts` beside the build in
// dist/, on port 3000. it has no secrets of its own: the ssr client calls the server at CADENCE_API_INTERNAL with the
// reader's cookie, and the browser calls the public origin
import { createServer } from 'node:http'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { quietConsole } from './console.ts'
import { createHandler } from './handler.ts'
import { createLog, errorClass } from './log.ts'
import { loadStaticFiles } from './static-files.ts'

const PORT = 3000
// above traefik's idle time for the connections it keeps to a backend (90 s), so it never sends on one node closed
const KEEP_ALIVE_MS = 95_000
const STOP_MS = 10_000

interface ServerEntry {
  fetch: (request: Request) => Promise<Response>
}

const log = createLog(line => process.stdout.write(line))

async function main() {
  quietConsole(log)
  process.removeAllListeners('warning')
  process.on('warning', warning => log('WARN', 'node_warning', {
    warning: errorClass(warning),
  }))
  for (const event of ['uncaughtException', 'unhandledRejection'] as const) {
    process.on(event, (error) => {
      log('ERROR', 'dashboard_crashed', {
        event,
        error: errorClass(error),
      })
      process.exit(1)
    })
  }

  // the ssr client checks it at each request; named here so a stack without it fails at start, the value never shown
  if (!process.env.CADENCE_API_INTERNAL) {
    log('ERROR', 'config_missing', { variable: 'CADENCE_API_INTERNAL' })
    process.exit(2)
  }

  const dist = new URL('../dist/', import.meta.url)
  const files = await loadStaticFiles(fileURLToPath(new URL('client/', dist)))
  const entry = (await import(new URL('server/server.js', dist).href) as { default: ServerEntry }).default

  const server = createServer({
    keepAliveTimeout: KEEP_ALIVE_MS,
    headersTimeout: KEEP_ALIVE_MS + 5_000,
  }, createHandler({
    files,
    render: request => entry.fetch(request),
    log,
  }))
  server.listen(PORT, '0.0.0.0', () => log('INFO', 'dashboard_listening', { port: PORT, files: files.size }))

  for (const signal of ['SIGTERM', 'SIGINT'] as const) {
    process.once(signal, () => {
      log('INFO', 'dashboard_stopping', { signal })
      server.close(() => process.exit(0))
      server.closeIdleConnections()
      setTimeout(() => process.exit(0), STOP_MS).unref()
    })
  }
}

main().catch((error: unknown) => {
  log('ERROR', 'dashboard_failed', {
    error: errorClass(error),
  })
  process.exit(1)
})
