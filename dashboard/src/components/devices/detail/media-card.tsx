import type { Failure } from './media/use-media-commands'
import type { CapabilityView, MediaSession } from '@/api/gen'
import type { Tone } from '@/components/data/tone'
import { useQuery } from '@tanstack/react-query'
import { ExternalLinkIcon, MusicIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { API_PATH } from '@/api/client'
import { useApi } from '@/api/use-api'
import { ErrorState, LoadingLines } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { useNow } from '@/hooks/use-now'
import { deviceMediaQueryOptions } from '@/lib/devices'
import { webLink } from '@/lib/music'
import { Queue } from './media/queue'
import { SeekBar } from './media/seek-bar'
import { Transport } from './media/transport'
import { useMediaCommands } from './media/use-media-commands'
import { VideoChoices } from './media/video-choices'
import { Volume } from './media/volume'

const STATES: Record<MediaSession['state'], { label: string, tone: Tone }> = {
  playing: { label: 'Playing', tone: 'success' },
  paused: { label: 'Paused', tone: 'neutral' },
  buffering: { label: 'Buffering', tone: 'info' },
  idle: { label: 'Stopped', tone: 'neutral' },
  unspecified: { label: 'Unknown', tone: 'neutral' },
}

const SOURCES: Record<MediaSession['source'], string> = {
  soloist: 'Spotify Connect',
  airplay: 'AirPlay',
  bluetooth: 'Bluetooth',
  url: 'URL',
  stream: 'Stream',
  video: 'Video',
  screencast: 'Screen mirroring',
  external: 'Another app',
  group: 'Multiroom group',
  unspecified: '',
}

// an episode's place, as the tv shows it: "S1 · E5"; a season alone or an episode alone as it is
function episodeLabel(metadata: MediaSession['metadata']): string {
  const parts = [
    metadata.season
      ? `S${metadata.season}`
      : '',
    metadata.episode
      ? `E${metadata.episode}`
      : '',
  ].filter(Boolean)
  return parts.join(' · ')
}

// the second line under the title: a song's artist and album, an episode's show and place
function subtitle(metadata: MediaSession['metadata']): string {
  if (metadata.series || metadata.kind === 'episode') {
    return [
      metadata.series,
      episodeLabel(metadata),
    ].filter(Boolean).join(' · ')
  }
  return [metadata.artist, metadata.album].filter(Boolean).join(' · ')
}

const FAILED: Record<string, string> = {
  pause: 'Pause',
  resume: 'Play',
  stop: 'Stop',
  next: 'Next',
  previous: 'Previous',
  shuffle: 'Shuffle',
  repeat: 'Repeat',
  jump: 'Playing from the queue',
}

// where the track is now: the device's stamp moved on at its rate while it plays (only once the page runs, so the ssr
// server and the browser render the stamp itself)
function position(session: MediaSession, now: number | null): number {
  const moving = session.state === 'playing' && session.positionAt !== undefined && now !== null
  const at = moving
    ? session.positionMs + (now - Date.parse(session.positionAt!)) * session.rate
    : session.positionMs
  return Math.max(0, session.durationMs > 0
    ? Math.min(at, session.durationMs)
    : at)
}

// the session's cover through the server (get-device-artwork, same origin, the owner's cookie): h pins the image to
// the hash the session named, so a newer session's cover never shows under an older one's; the note icon stands in
// while there is none or it fails. keyed by the hash, so a new cover is tried afresh
function Artwork({ deviceId, hash }: { deviceId: string, hash: string }) {
  const [failed, setFailed] = useState(false)
  const box = 'grid size-16 shrink-0 place-items-center overflow-hidden rounded-md border bg-muted text-muted-foreground'
  if (!hash || failed) {
    return (
      <div aria-hidden="true" className={box}>
        <MusicIcon className="size-6" />
      </div>
    )
  }
  return (
    <div aria-hidden="true" className={box}>
      <img
        src={`${API_PATH}/devices/${encodeURIComponent(deviceId)}/artwork?h=${hash}`}
        alt=""
        className="size-full object-cover"
        onError={() => setFailed(true)}
      />
    </div>
  )
}

// the song's page at its provider, in a new tab that cannot reach back into this one
function OpenLink({ session }: { session: MediaSession }) {
  const href = webLink(session.webUrl)
  if (!href)
    return null
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={`Open in ${session.provider || 'its provider'} (new tab)`}
      className="inline-flex items-center gap-1 text-sm text-foreground underline decoration-input underline-offset-4 hover:decoration-foreground"
    >
      Open
      <ExternalLinkIcon aria-hidden="true" className="size-3.5" />
    </a>
  )
}

function Session({ deviceId, session, commands }: {
  deviceId: string
  session: MediaSession
  commands: ReturnType<typeof useMediaCommands>
}) {
  const now = useNow()
  const { title } = session.metadata
  const second = subtitle(session.metadata)
  const state = STATES[session.state]
  // a song of the server's queue names where it was found; any other session the provider the device names, else its
  // source
  const source = session.provider || session.metadata.providerName || SOURCES[session.source]
  return (
    <div className="flex flex-col gap-4">
      <div className="flex gap-3">
        {/* the server fetches or keeps the cover: the page loads nothing from the source's own hosts */}
        <Artwork key={session.metadata.artworkHash} deviceId={deviceId} hash={session.metadata.artworkHash} />
        <div className="min-w-0">
          <p className="truncate font-medium">{title || 'Untitled'}</p>
          {second && <p className="truncate text-sm text-muted-foreground">{second}</p>}
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1">
            <StatusBadge tone={state.tone} label={state.label} detail={source || undefined} />
            <OpenLink session={session} />
          </div>
        </div>
      </div>
      {session.durationMs > 0 && (
        <SeekBar
          positionMs={position(session, now)}
          durationMs={session.durationMs}
          onSeek={session.actions.includes('seek')
            ? commands.seek
            : undefined}
        />
      )}
      <Transport session={session} commands={commands} />
      {session.source === 'video' && <VideoChoices deviceId={deviceId} session={session} />}
    </div>
  )
}

function FailureLine({ failure }: { failure: Failure | undefined }) {
  if (!failure)
    return null
  return <p role="alert" className="text-xs text-destructive">{`${FAILED[failure.what] ?? 'The command'} failed. ${failure.message}`}</p>
}

// the device's media session, kept by the server in memory only and refreshed by the feed's media events, as a small
// player: its controls where the session offers them, the server's queue, and the device's level
export function MediaCard({ deviceId, output }: { deviceId: string, output?: CapabilityView }) {
  const api = useApi()
  const titleId = useId()
  const media = useQuery(deviceMediaQueryOptions(api, deviceId))
  const commands = useMediaCommands(deviceId)
  const session = media.data && 'sessionId' in media.data
    ? media.data
    : undefined
  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader>
        <h2 id={titleId} className="font-heading text-base font-semibold">Media</h2>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {media.isPending && <LoadingLines lines={2} label="Loading the media session" />}
        {media.isError && <ErrorState title="The media session could not be read" error={media.error} onRetry={() => void media.refetch()} className="px-0 py-4" />}
        {media.data && (session
          ? <Session deviceId={deviceId} session={session} commands={commands} />
          : <p className="text-sm text-muted-foreground">Nothing is playing.</p>)}
        <FailureLine failure={commands.failure} />
        <Queue deviceId={deviceId} commands={commands} />
        <Volume deviceId={deviceId} output={output} />
      </CardContent>
    </Card>
  )
}
