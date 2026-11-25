import type { ReactNode } from 'react'
import type { CapabilityView } from '@/api/gen'
import type { Tone } from '@/components/data/tone'
import { useId } from 'react'
import { StatusBadge } from '@/components/data/status-badge'
import { isObject } from '@/components/forms/json-pointer'
import { CapabilityConfig } from '../detail/capability-config'
import { capabilitySchemas } from '../detail/capability-schemas'

// the screen's capabilities whose config the owner sets here, in this order, by what each is to them
const SCREEN_SETTINGS: readonly { id: string, title: string }[] = [
  { id: 'display.screen', title: 'Screen' },
  { id: 'media.video', title: 'Video player' },
  { id: 'input.remote', title: 'Remote' },
  // the delay trim (delayMs) and, on a screen, whose volume the level is (volume.mode)
  { id: 'audio.output', title: 'Sound' },
]

function Block({ title, children }: { title: string, children: ReactNode }) {
  const titleId = useId()
  return (
    <div role="group" aria-labelledby={titleId} className="flex flex-col gap-2">
      <h4 id={titleId} className="text-sm font-semibold">{title}</h4>
      {children}
    </div>
  )
}

function Config({ deviceId, capability }: { deviceId: string, capability: CapabilityView }) {
  return <CapabilityConfig deviceId={deviceId} capability={capability} schema={capabilitySchemas(capability).config} />
}

export function ScreenSettings({ deviceId, capabilities }: { deviceId: string, capabilities: readonly CapabilityView[] }) {
  const parts = SCREEN_SETTINGS.flatMap((s) => {
    const c = capabilities.find(x => x.id === s.id)
    return c
      ? [
          { ...s, capability: c },
        ]
      : []
  })
  return (
    <div className="flex flex-col gap-5">
      {parts.map(p => (
        <Block key={p.id} title={p.title}>
          <Config deviceId={deviceId} capability={p.capability} />
        </Block>
      ))}
    </div>
  )
}

const AIRPLAY_STATES: Record<string, { label: string, tone: Tone }> = {
  off: { label: 'Off', tone: 'neutral' },
  starting: { label: 'Starting', tone: 'info' },
  ready: { label: 'Ready', tone: 'success' },
  failed: { label: 'Failed', tone: 'danger' },
}

// AirPlay on the screen (media.screencast): whether it runs, who mirrors to it now, and its config (name, pin, quality)
export function AirplaySettings({ deviceId, capability }: { deviceId: string, capability: CapabilityView }) {
  const state = isObject(capability.state)
    ? capability.state
    : {}
  const phase = typeof state.state === 'string'
    ? state.state
    : ''
  const shown = AIRPLAY_STATES[phase] ?? { label: 'Unknown', tone: 'neutral' as Tone }
  const mirroring = state.mirroring === true
  const client = typeof state.clientName === 'string'
    ? state.clientName
    : ''
  const code = typeof state.code === 'string'
    ? state.code
    : ''
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <StatusBadge tone={shown.tone} label={shown.label} detail={code || undefined} />
        <span>
          {mirroring
            ? (client
                ? `Mirroring from ${client}`
                : 'Mirroring')
            : 'Not mirroring'}
        </span>
      </div>
      <CapabilityConfig deviceId={deviceId} capability={capability} schema={capabilitySchemas(capability).config} />
    </div>
  )
}
