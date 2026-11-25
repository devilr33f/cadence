import type { Client } from '@/api/gen/client'
import { getDeviceMusicOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

// the server's music queue for a device (memory only); the feed's media events mark it stale
export function deviceMusicQueryOptions(api: Client, id: string) {
  return sharedKey(getDeviceMusicOptions({
    client: api,
    path: { id },
  }))
}

// a link the page may open: a song's page is the provider's, and only a web address is one (never javascript: or data:)
export function webLink(url: string | undefined): string | undefined {
  if (!url)
    return undefined
  try {
    const parsed = new URL(url)
    return parsed.protocol === 'https:' || parsed.protocol === 'http:'
      ? parsed.href
      : undefined
  }
  catch {
    return undefined
  }
}
