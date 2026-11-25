import { readdir, stat } from 'node:fs/promises'
import { extname, join, relative, sep } from 'node:path'

export interface StaticFile {
  file: string
  type: string
  size: number
  cacheControl: string
}

const TYPES: Record<string, string> = {
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json',
  '.map': 'application/json',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
  '.webp': 'image/webp',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.txt': 'text/plain; charset=utf-8',
  '.webmanifest': 'application/manifest+json',
}

// vite's assets/ carry their content's hash in the name, so a name never changes what it holds
const HASHED = '/assets/'
const IMMUTABLE = 'public, max-age=31536000, immutable'
const SHORT = 'public, max-age=3600'

// every file of the client build by its url path, read once at start: a request names one of these exactly or none,
// so no path it sends ever reaches the file system
export async function loadStaticFiles(root: string): Promise<Map<string, StaticFile>> {
  const files = new Map<string, StaticFile>()
  for (const entry of await readdir(root, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile())
      continue
    const file = join(entry.parentPath, entry.name)
    const path = `/${relative(root, file).split(sep).join('/')}`
    files.set(path, {
      file,
      type: TYPES[extname(entry.name).toLowerCase()] ?? 'application/octet-stream',
      size: (await stat(file)).size,
      cacheControl: path.startsWith(HASHED)
        ? IMMUTABLE
        : SHORT,
    })
  }
  return files
}
