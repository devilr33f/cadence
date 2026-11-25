import { bytesText } from '@/lib/units'

export const SERVICE_SERVER = 'cadence-server'
export const SERVICE_DEVICE = 'cadence-device'

// a span's length as a profiler says it: 0.42 ms, 3.4 ms, 120 ms, 1.24 s, 2 m 03 s
export function spanDurationText(ms: number): string {
  const value = Math.max(0, ms)
  if (value < 1)
    return `${Number(value.toFixed(2))} ms`
  if (value < 10)
    return `${Number(value.toFixed(1))} ms`
  if (value < 1000)
    return `${Math.round(value)} ms`
  if (value < 60_000)
    return `${Number((value / 1000).toPrecision(3))} s`
  const seconds = Math.floor(value / 1000)
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor(seconds / 60) % 60
  const rest = String(seconds % 60).padStart(2, '0')
  return hours > 0
    ? `${hours} h ${String(minutes).padStart(2, '0')} m`
    : `${minutes} m ${rest} s`
}

// a transfer rate in bytes a second; nothing moving says so
export function rateText(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0)
    return '0 B/s'
  return `${bytesText(bytesPerSecond)}/s`
}

// a service as the page names it: the server or a speaker, else its own name
export function serviceLabel(service: string): string {
  switch (service) {
    case SERVICE_SERVER:
      return 'Server'
    case SERVICE_DEVICE:
      return 'Device'
    default:
      return service
  }
}

// an attribute's value as one line of text: strings as they are, the rest as json
export function attrText(value: unknown): string {
  if (typeof value === 'string')
    return value
  if (value === undefined)
    return ''
  return JSON.stringify(value)
}
