import type { FormEvent } from 'react'
import type { PlayResult } from '@/api/gen'
import { useQueryClient } from '@tanstack/react-query'
import { LoaderCircleIcon, PlayIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { playDeviceVideo } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { deviceMediaQueryOptions } from '@/lib/devices'
import { deviceVideoQueryOptions, playRefusal } from './tv'

const VIEWS: Record<NonNullable<PlayResult['view']>, string> = {
  video: '',
  audio: ' as audio only',
  visualizer: ' with the visualizer',
}

// what the tv now plays, in words: its title as YouTube names it, its quality or its view
function playingText(r: PlayResult): string {
  const what = r.title
    ? `«${r.title}»`
    : ''
  const how = r.view && r.view !== 'video'
    ? VIEWS[r.view]
    : r.height
      ? ` at ${r.height}p`
      : ''
  return `Playing${what
    ? ` ${what}`
    : ''}${how}.`
}

type Played = { status: 'idle' | 'pending' } | { status: 'playing', text: string } | { status: 'failed', message: string }

// a YouTube link played on the tv at the best quality it takes, through the server's relay; the tv shows its resolving
// step meanwhile. the link is sent once and kept nowhere
export function PlayOnTv({ deviceId }: { deviceId: string }) {
  const api = useApi()
  const queryClient = useQueryClient()
  const fieldId = useId()
  const [url, setUrl] = useState('')
  const [played, setPlayed] = useState<Played>({ status: 'idle' })

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (url.trim() === '' || played.status === 'pending')
      return
    setPlayed({ status: 'pending' })
    try {
      const { data } = await playDeviceVideo({
        client: api,
        path: { id: deviceId },
        body: {
          url: url.trim(),
          screenOnly: true,
        },
        throwOnError: true,
      })
      setUrl('')
      setPlayed({
        status: 'playing',
        text: playingText(data),
      })
    }
    catch (failure) {
      setPlayed({
        status: 'failed',
        message: playRefusal(failure),
      })
    }
    finally {
      void queryClient.invalidateQueries({ queryKey: deviceMediaQueryOptions(api, deviceId).queryKey })
      void queryClient.invalidateQueries({ queryKey: deviceVideoQueryOptions(api, deviceId).queryKey })
    }
  }

  return (
    <form method="post" noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-2">
      <Label htmlFor={fieldId}>YouTube link</Label>
      <div className="flex gap-2">
        <Input
          id={fieldId}
          value={url}
          inputMode="url"
          placeholder="https://youtu.be/…"
          onChange={(e) => {
            setUrl(e.target.value)
            if (played.status !== 'pending')
              setPlayed({ status: 'idle' })
          }}
        />
        <Button type="submit" variant="outline" aria-disabled={url.trim() === '' || played.status === 'pending' || undefined}>
          {played.status === 'pending'
            ? <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />
            : <PlayIcon aria-hidden="true" />}
          Play on the TV
        </Button>
      </div>
      {played.status === 'playing' && <p role="status" className="text-sm text-muted-foreground">{played.text}</p>}
      {played.status === 'failed' && <p role="alert" className="text-sm text-destructive">{played.message}</p>}
    </form>
  )
}
