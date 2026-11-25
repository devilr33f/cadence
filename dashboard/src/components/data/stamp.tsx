import { stampText } from '@/lib/format-time'

// a moment that may be days ago, set as the timetable's figures: its day and minute in the household's zone
export function Stamp({ at, timeZone }: { at: string, timeZone: string | null }) {
  return <time dateTime={at} className="font-semibold">{stampText(at, timeZone)}</time>
}
