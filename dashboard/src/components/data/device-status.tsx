import type { Glyph } from './status-glyph'
import type { Tone } from './tone'
import { StatusBadge } from './status-badge'

export type DeviceState = 'online' | 'offline' | 'updating' | 'enrolling'

const STATES: Record<DeviceState, { label: string, tone: Tone, glyph?: Glyph }> = {
  online: { label: 'Online', tone: 'success' },
  offline: { label: 'Offline', tone: 'neutral' },
  updating: { label: 'Updating', tone: 'info' },
  enrolling: { label: 'Enrolling', tone: 'neutral', glyph: 'dashed' },
}

export function DeviceStatus({ status, appearance }: { status: DeviceState, appearance?: 'inline' | 'pill' }) {
  const { label, tone, glyph } = STATES[status]
  return <StatusBadge tone={tone} label={label} glyph={glyph} appearance={appearance} />
}
