import type { ReactNode } from 'react'
import type { Detail } from '@/api/gen'
import { useId } from 'react'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { PlayOnTv } from './play-on-tv'
import { RemotePad } from './remote-pad'
import { AirplaySettings, ScreenSettings } from './screen-settings'

function Panel({ title, children }: { title: string, children: ReactNode }) {
  const titleId = useId()
  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader>
        <h3 id={titleId} className="font-heading text-base font-semibold">{title}</h3>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">{children}</CardContent>
    </Card>
  )
}

// the tv's own part of its page: its remote, a video to play on it (its quality, audio and view are the Media card's,
// beside the session they belong to), and the settings of
// its screen, player, remote, sound and AirPlay
export function TvSection({ device }: { device: Detail }) {
  const capabilities = device.capabilities ?? []
  const screencast = capabilities.find(c => c.id === 'media.screencast')
  const remote = capabilities.some(c => c.id === 'input.remote')
  const video = capabilities.some(c => c.id === 'media.video')
  return (
    <div className="grid gap-4 md:grid-cols-2">
      {remote && (
        <Panel title="Remote">
          <RemotePad deviceId={device.id} />
        </Panel>
      )}
      {video && (
        <Panel title="Play on the TV">
          <PlayOnTv deviceId={device.id} />
        </Panel>
      )}
      <Panel title="Screen settings">
        <ScreenSettings deviceId={device.id} capabilities={capabilities} />
      </Panel>
      {screencast && (
        <Panel title="AirPlay">
          <AirplaySettings deviceId={device.id} capability={screencast} />
        </Panel>
      )}
    </div>
  )
}
