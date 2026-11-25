// what a time shows until the household's zone is known: the ssr server and the browser render the same text
export const CLOCK_PLACEHOLDER = '--:--:--'

// times are worded in the household's zone (the timeZone setting), always given, so the ssr server (utc) and the
// browser (its local zone) print the same text
export function formatClock(at: string, timeZone: string, withMillis = false): string {
  return new Intl.DateTimeFormat('en-GB', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    fractionalSecondDigits: withMillis ? 3 : undefined,
    hourCycle: 'h23',
    timeZone,
  }).format(new Date(at))
}

// a time for a page whose zone may still be loading (null)
export function clockText(at: string, timeZone: string | null, withMillis = false): string {
  if (timeZone === null) {
    return withMillis
      ? `${CLOCK_PLACEHOLDER}.---`
      : CLOCK_PLACEHOLDER
  }
  return formatClock(at, timeZone, withMillis)
}

export const STAMP_PLACEHOLDER = '-- --- --:--'

// a moment that may be days ago (a device last seen): its day and minute in the household's zone, with its second where
// moments come close together (a conversation's turns)
export function stampText(at: string, timeZone: string | null, withSeconds = false): string {
  if (timeZone === null) {
    return withSeconds
      ? `${STAMP_PLACEHOLDER}:--`
      : STAMP_PLACEHOLDER
  }
  return new Intl.DateTimeFormat('en-GB', {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
    second: withSeconds ? '2-digit' : undefined,
    hourCycle: 'h23',
    timeZone,
  }).format(new Date(at))
}

// a length of media as a player shows it: m:ss, or h:mm:ss from an hour
export function durationText(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor(total / 60) % 60
  const seconds = String(total % 60).padStart(2, '0')
  return hours > 0
    ? `${hours}:${String(minutes).padStart(2, '0')}:${seconds}`
    : `${minutes}:${seconds}`
}
