import type { Detail } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { getDeviceVideoOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

// a device is a tv when it reports display.screen
export function isScreen(device: Pick<Detail, 'capabilities'>): boolean {
  return device.capabilities?.some(c => c.id === 'display.screen') === true
}

export function deviceVideoQueryOptions(api: Client, id: string) {
  return sharedKey(getDeviceVideoOptions({
    client: api,
    path: { id },
  }))
}

// play-device-video's refusals, in words
const REFUSALS: Readonly<Record<string, string>> = {
  invalid_url: 'That is not a YouTube video link: a watch, shorts, live or youtu.be link.',
  no_screen: 'This device has no screen to show a video on.',
  no_player: 'The screen has no video player.',
  device_offline: 'The screen is offline.',
  not_set_up: 'No video player is set up on the server (the musicPlayer setting).',
  unplayable: 'YouTube offers nothing this screen can play for that video.',
  sign_in_required: 'YouTube wants a signed-in viewer for that video.',
}

export function playRefusal(failure: unknown): string {
  if (failure instanceof ApiError && Object.hasOwn(REFUSALS, failure.error))
    return `${REFUSALS[failure.error]} (${failure.error})`
  return describeError(failure)
}
