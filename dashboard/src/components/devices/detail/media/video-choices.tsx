import type { AudioView, MediaSession, RenditionView, UpdateDeviceVideoData, VideoView } from '@/api/gen'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckIcon } from 'lucide-react'
import { useState } from 'react'
import { describeError } from '@/api/describe-error'
import { updateDeviceVideo } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { deviceVideoQueryOptions } from '@/components/devices/tv/tv'
import { Button } from '@/components/ui/button'
import { deviceMediaQueryOptions } from '@/lib/devices'

type Change = NonNullable<UpdateDeviceVideoData['body']>

const VIEWS: Record<VideoView['views'][number], string> = {
  video: 'Video',
  audio: 'Audio only',
  visualizer: 'Audio + visualizer',
}

// an audio language as YouTube names it, with whether it is the video's own or a dubbing
function audioLabel(a: AudioView): string {
  const name = a.label || (a.language === 'und'
    ? 'Audio'
    : a.language.toUpperCase())
  if (a.original)
    return `${name} · original`
  if (a.dubbed)
    return `${name} · dub`
  return name
}

function Choices<T>({ label, items, id, name, detail, selected, pending, onPick }: {
  label: string
  items: T[]
  id: (item: T) => string
  name: (item: T) => string
  detail?: (item: T) => string | undefined
  selected: (item: T) => boolean
  pending: boolean
  onPick: (item: T) => void
}) {
  return (
    <div role="group" aria-label={label} className="flex flex-col gap-1.5">
      <p className="text-sm font-medium">{label}</p>
      <div className="flex flex-wrap gap-1.5">
        {items.map((item) => {
          const on = selected(item)
          const extra = detail?.(item)
          return (
            <Button
              key={id(item)}
              type="button"
              size="sm"
              variant={on ? 'secondary' : 'outline'}
              aria-pressed={on}
              aria-disabled={pending || undefined}
              onClick={() => {
                if (!on && !pending)
                  onPick(item)
              }}
            >
              {on && <CheckIcon aria-hidden="true" />}
              {name(item)}
              {extra && <span className="text-xs text-muted-foreground">{extra}</span>}
            </Button>
          )
        })}
      </div>
    </div>
  )
}

function renditionDetail(r: RenditionView): string | undefined {
  return [r.vcodec, r.hdr].filter(Boolean).join(' · ') || undefined
}

// the choices of the video the server plays as this session (memory only): its view (a music video's), quality and
// audio language, each switched from where it plays; shown only while the device's session is the one the server
// started, so a quality list never stands without the video it belongs to
export function VideoChoices({ deviceId, session }: { deviceId: string, session: MediaSession }) {
  const api = useApi()
  const queryClient = useQueryClient()
  const video = useQuery({
    ...deviceVideoQueryOptions(api, deviceId),
    enabled: session.source === 'video',
  })
  const [pending, setPending] = useState(false)
  const [failure, setFailure] = useState<string>()
  const v = video.data
  if (!v || !v.sessionId || v.sessionId !== session.sessionId)
    return null

  async function pick(change: Change) {
    setPending(true)
    setFailure(undefined)
    try {
      const { data } = await updateDeviceVideo({
        client: api,
        path: { id: deviceId },
        body: change,
        throwOnError: true,
      })
      queryClient.setQueryData(deviceVideoQueryOptions(api, deviceId).queryKey, data)
    }
    catch (error) {
      setFailure(describeError(error))
    }
    finally {
      setPending(false)
      void queryClient.invalidateQueries({ queryKey: deviceVideoQueryOptions(api, deviceId).queryKey })
      void queryClient.invalidateQueries({ queryKey: deviceMediaQueryOptions(api, deviceId).queryKey })
    }
  }

  const audioView = v.view === 'audio' || v.view === 'visualizer'
  return (
    <div className="flex flex-col gap-3" aria-busy={pending || undefined}>
      {v.description && <p className="line-clamp-3 text-sm whitespace-pre-line text-muted-foreground">{v.description}</p>}
      {v.views.length > 1 && (
        <Choices
          label="View"
          items={v.views}
          id={x => x}
          name={x => VIEWS[x]}
          selected={x => x === v.view}
          pending={pending}
          onPick={x => void pick({ view: x })}
        />
      )}
      {v.renditions.length > 1 && (
        <Choices
          label="Quality"
          items={v.renditions}
          id={r => r.id}
          name={r => r.id}
          detail={renditionDetail}
          selected={r => r.selected && !audioView}
          pending={pending}
          onPick={r => void pick({ rendition: r.id })}
        />
      )}
      {v.audio.length > 1 && (
        <Choices
          label="Audio"
          items={v.audio}
          id={a => a.id}
          name={audioLabel}
          selected={a => a.selected === true}
          pending={pending}
          onPick={a => void pick({ audio: a.id })}
        />
      )}
      {failure && <p role="alert" className="text-sm text-destructive">{`The video was not switched. ${failure}`}</p>}
    </div>
  )
}
