const WALL = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/

function wallParts(at: number, zone: string): Record<string, number> {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: zone,
    hourCycle: 'h23',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).formatToParts(new Date(at))
  return Object.fromEntries(parts.filter(p => p.type !== 'literal').map(p => [
    p.type,
    Number(p.value),
  ]))
}

// how far the zone's clock is ahead of utc at an instant
export function zoneOffsetMs(at: number, zone: string): number {
  const p = wallParts(at, zone)
  const wall = Date.UTC(p.year!, p.month! - 1, p.day!, p.hour!, p.minute!, p.second!)
  return wall - Math.floor(at / 1000) * 1000
}

// a wall time in the household's zone (a datetime-local field's value) as the instant it names; the offset is read
// twice, so a time just across a summer-time change takes the offset in force then
export function zonedToInstant(local: string, zone: string): string | undefined {
  const match = WALL.exec(local)
  if (!match)
    return undefined
  const [, year, month, day, hour, minute, second] = match.map(Number) as number[]
  const wall = Date.UTC(year!, month! - 1, day!, hour!, minute!, Number.isNaN(second)
    ? 0
    : second!)
  const first = wall - zoneOffsetMs(wall, zone)
  return new Date(wall - zoneOffsetMs(first, zone)).toISOString()
}

// an instant as the zone's wall time, as a datetime-local field holds it
export function instantToZoned(at: string, zone: string): string {
  const p = wallParts(Date.parse(at), zone)
  const two = (n: number | undefined) => String(n).padStart(2, '0')
  return `${p.year}-${two(p.month)}-${two(p.day)}T${two(p.hour)}:${two(p.minute)}`
}
