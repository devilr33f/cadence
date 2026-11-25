import type { MediaCommands } from './use-media-commands'
import type { Song, View } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { ChevronDownIcon, ListMusicIcon, Repeat1Icon, RepeatIcon, ShuffleIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { durationText } from '@/lib/format-time'
import { deviceMusicQueryOptions } from '@/lib/music'

type Repeat = View['repeat']

const NEXT_REPEAT: Record<Repeat, Repeat> = { off: 'all', all: 'one', one: 'off' }
const REPEAT_WORDS: Record<Repeat, string> = { off: 'off', all: 'the queue', one: 'this song' }

function songLine(song: Song): string {
  return [song.artist, song.album].filter(Boolean).join(' · ')
}

function songName(song: Song): string {
  return song.artist
    ? `${song.title} by ${song.artist}`
    : song.title
}

function Modes({ music, commands }: { music: View, commands: MediaCommands }) {
  const busy = commands.running !== undefined
  const next = NEXT_REPEAT[music.repeat]
  return (
    <div role="group" aria-label="Play order" className="flex items-center gap-1">
      <Button
        type="button"
        variant={music.shuffle
          ? 'secondary'
          : 'ghost'}
        size="icon-sm"
        aria-label="Shuffle"
        aria-pressed={music.shuffle}
        aria-disabled={busy || undefined}
        onClick={() => {
          if (!busy)
            void commands.modes({ shuffle: !music.shuffle })
        }}
      >
        <ShuffleIcon aria-hidden="true" />
      </Button>
      {/* one button through three modes: its name says the mode in force, its description the one a press sets */}
      <Button
        type="button"
        variant={music.repeat === 'off'
          ? 'ghost'
          : 'secondary'}
        size="icon-sm"
        aria-label={`Repeat: ${REPEAT_WORDS[music.repeat]}`}
        title={`Repeat ${REPEAT_WORDS[next]} next`}
        aria-disabled={busy || undefined}
        onClick={() => {
          if (!busy)
            void commands.modes({ repeat: next })
        }}
      >
        {music.repeat === 'one'
          ? <Repeat1Icon aria-hidden="true" />
          : <RepeatIcon aria-hidden="true" />}
      </Button>
    </div>
  )
}

function SongText({ song }: { song: Song }) {
  const line = songLine(song)
  return (
    <span className="flex min-w-0 flex-1 flex-col text-left">
      <span className="truncate text-sm">{song.title}</span>
      {line && <span className="truncate text-xs text-muted-foreground">{line}</span>}
    </span>
  )
}

function Duration({ song }: { song: Song }) {
  return song.durationS
    ? <span className="text-xs text-muted-foreground tabular-nums">{durationText(song.durationS * 1000)}</span>
    : null
}

function Lists({ music, commands }: { music: View, commands: MediaCommands }) {
  const upId = useId()
  const pastId = useId()
  const busy = commands.running !== undefined
  return (
    <div className="flex flex-col gap-3">
      {music.upcoming.length > 0 && (
        <section aria-labelledby={upId} className="flex flex-col gap-1">
          <h3 id={upId} className="text-xs font-medium text-muted-foreground">Up next</h3>
          <ol className="flex flex-col">
            {music.upcoming.map((song, i) => (
              // eslint-disable-next-line react/no-array-index-key -- a queue may hold one song twice; its place is its key
              <li key={i}>
                <button
                  type="button"
                  aria-label={`Play ${songName(song)}`}
                  aria-disabled={busy || undefined}
                  onClick={() => {
                    if (!busy)
                      void commands.jump(i + 1)
                  }}
                  className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 hover:bg-muted aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
                >
                  <SongText song={song} />
                  <Duration song={song} />
                </button>
              </li>
            ))}
          </ol>
        </section>
      )}
      {music.history.length > 0 && (
        <section aria-labelledby={pastId} className="flex flex-col gap-1">
          <h3 id={pastId} className="text-xs font-medium text-muted-foreground">Recently played</h3>
          <ol className="flex flex-col">
            {music.history.map((song, i) => (
              // eslint-disable-next-line react/no-array-index-key -- the history may hold one song twice
              <li key={i} className="flex items-center gap-2 px-2 py-1.5 text-muted-foreground">
                <SongText song={song} />
                <Duration song={song} />
              </li>
            ))}
          </ol>
        </section>
      )}
    </div>
  )
}

// the server's music queue: its play order and, behind a disclosure, the songs to come (a press plays one) and the
// last ones played. shown only while the queue plays or holds a song a skip moves from
export function Queue({ deviceId, commands }: { deviceId: string, commands: MediaCommands }) {
  const api = useApi()
  const panelId = useId()
  const [open, setOpen] = useState(false)
  const query = useQuery(deviceMusicQueryOptions(api, deviceId))
  if (query.isError) {
    return (
      <div role="alert" className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <span>The music queue could not be read.</span>
        <Button type="button" variant="ghost" size="xs" onClick={() => void query.refetch()}>Try again</Button>
      </div>
    )
  }
  const music = query.data
  if (!music || (!music.active && !music.current))
    return null
  const count = music.upcoming.length + music.history.length
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <Modes music={music} commands={commands} />
        {count > 0 && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-expanded={open}
            aria-controls={panelId}
            onClick={() => setOpen(!open)}
          >
            <ListMusicIcon aria-hidden="true" />
            Queue
            <span aria-hidden="true" className="text-muted-foreground tabular-nums">{music.upcoming.length}</span>
            <span className="sr-only">{`, ${music.upcoming.length} up next`}</span>
            <ChevronDownIcon aria-hidden="true" className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
          </Button>
        )}
      </div>
      {open && count > 0 && (
        <div id={panelId}>
          <Lists music={music} commands={commands} />
        </div>
      )}
    </div>
  )
}
