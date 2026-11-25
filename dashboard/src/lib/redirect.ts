const BASE = 'http://cadence.invalid'

function hasControl(value: string): boolean {
  return [...value].some((c) => {
    const code = c.charCodeAt(0)
    return code < 0x20 || code === 0x7F
  })
}

// where a sign-in returns to: a path on this origin, never the login itself; anything else (an absolute url, //host,
// /\host, a tab the url parser would drop, dot segments that normalize to //host) goes home
export function safeRedirect(value: unknown): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || value.includes('\\') || hasControl(value))
    return '/'
  let url: URL
  try {
    url = new URL(value, BASE)
  }
  catch {
    return '/'
  }
  // the parser resolves /./ /../ and %2e: what comes out is what the browser would follow
  if (url.origin !== BASE || url.pathname.startsWith('//') || url.pathname.includes('\\') || url.pathname === '/login')
    return '/'
  return `${url.pathname}${url.search}${url.hash}`
}
