import type { DeviceView } from '@/api/gen'

export const KITCHEN = '6f1c2a4e-1b0d-4c3e-9a51-0d2b7e8f4a10'
export const BEDROOM = '9a3e5c71-2d4f-4b6a-8c0e-1f2a3b4c5d6e'

export const DEVICES: DeviceView[] = [
  {
    id: KITCHEN,
    name: 'Kitchen',
    model: 'sim',
    room: 'Kitchen',
    status: 'enrolled',
    online: true,
    lastSeenAt: '2026-11-12T15:00:00Z',
  },
  {
    id: BEDROOM,
    name: 'Bedroom',
    model: 'yandex-pickle',
    room: '',
    status: 'enrolled',
    online: false,
    lastSeenAt: '2026-11-12T14:00:00Z',
  },
]

// the query string of a request, as an object
export function queryOf(request: Request): Record<string, string> {
  return Object.fromEntries(new URL(request.url).searchParams)
}
