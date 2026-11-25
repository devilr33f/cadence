import { API_PATH } from '@/api/client'

// the page's own origin, as ws: or wss:, so the browser sends the session cookie and the server's Origin
export function liveUrl(location: Pick<Location, 'href'> = window.location): string {
  const url = new URL(`${API_PATH}/live`, location.href)
  url.protocol = url.protocol === 'https:'
    ? 'wss:'
    : 'ws:'
  return url.href
}
