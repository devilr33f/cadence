import type { EventJson, NoticeJson } from '@/api/gen'
import type { TimelineEntry } from '@/components/data/timeline'
import { Stamp } from '@/components/data/stamp'
import { Timeline } from '@/components/data/timeline'
import { useDeviceNames } from '@/hooks/use-device-names'
import { useTimeZone } from '@/lib/time-zone'
import { STATUS } from './task-words'

// an event kind as words: a status the task entered in the status's own words (Waiting for an answer), anything else as
// the server's token with its underscores spaced and its first letter raised
function kindText(kind: string): string {
  if (STATUS[kind])
    return STATUS[kind].label
  const words = kind.replaceAll('_', ' ')
  return words.charAt(0).toUpperCase() + words.slice(1)
}

function entryOf(event: EventJson, names: ReadonlyMap<string, string>): TimelineEntry {
  const answer = [event.decision, event.via && `via ${event.via}`].filter(Boolean).join(' ')
  const device = event.device && (names.get(event.device) ?? event.device)
  return {
    id: String(event.seq),
    at: event.at,
    kind: event.kind === 'failed'
      ? 'error'
      : 'task',
    text: answer
      ? `${kindText(event.kind)}: ${answer}`
      : kindText(event.kind),
    // an answer's own words (kept 14 days) and the speaker it came from, as text
    detail: (event.words || device) && (
      <div className="flex flex-col gap-1">
        {device && <span className="text-muted-foreground">{`From ${device}`}</span>}
        {event.words && <span className="break-words whitespace-pre-wrap">{event.words}</span>}
      </div>
    ),
  }
}

// the task's events as a stop list, newest on top
export function TaskHistory({ events }: { events: readonly EventJson[] }) {
  const timeZone = useTimeZone()
  const names = useDeviceNames()
  if (events.length === 0)
    return <p className="text-sm text-muted-foreground">Nothing has happened yet.</p>
  const entries = [...events].sort((a, b) => b.seq - a.seq).map(event => entryOf(event, names))
  return <Timeline entries={entries} timeZone={timeZone} label="Task history" />
}

// what the task told the household, and whether a speaker has said it yet (spoken updates wait out quiet hours)
export function TaskNotices({ notices }: { notices: readonly NoticeJson[] }) {
  const timeZone = useTimeZone()
  return (
    <ul className="flex flex-col divide-y rounded-lg border bg-card">
      {notices.map(notice => (
        <li key={notice.eventSeq} className="flex flex-col gap-1 px-4 py-3">
          <span className="break-words whitespace-pre-wrap">{notice.text}</span>
          <span className="text-sm text-muted-foreground">
            <Stamp at={notice.createdAt} timeZone={timeZone} />
            {' · '}
            {notice.deliveredAt === null
              ? <span>Not said yet</span>
              : (
                  <span>
                    {'said '}
                    <Stamp at={notice.deliveredAt} timeZone={timeZone} />
                  </span>
                )}
          </span>
        </li>
      ))}
    </ul>
  )
}
