import type { Detail } from '@/api/gen'
import { ActivityIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { EmptyState } from '@/components/data/states'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { DEFAULT_WINDOW, METRIC_WINDOWS } from '@/lib/metrics'
import { useTimeZone } from '@/lib/time-zone'
import { CpuCard, MemoryCard, TemperatureCard } from './metric-cards'
import { Processes } from './processes'
import { StorageCard } from './storage-card'

const METRICS_CAPABILITY = 'system.metrics'

// the device's round interval as its system.metrics config asks it, the device's default when none was set
function intervalOf(device: Detail): number {
  const desired = device.capabilities?.find(c => c.id === METRICS_CAPABILITY)?.desired
  const interval = typeof desired === 'object' && desired !== null
    ? (desired as Record<string, unknown>).intervalS
    : undefined
  return typeof interval === 'number'
    ? interval
    : 30
}

// a device's metrics over a window and its top processes; read only while the tab is open, and again as often as a
// chart's point spans. a device with no system.metrics capability sends none: telemetry is off on it
export function Health({ device }: { device: Detail }) {
  const timeZone = useTimeZone()
  const [windowId, setWindowId] = useState<string>(DEFAULT_WINDOW.id)
  const window = METRIC_WINDOWS.find(w => w.id === windowId) ?? DEFAULT_WINDOW
  const windowFieldId = useId()

  if (!device.capabilities?.some(c => c.id === METRICS_CAPABILITY)) {
    return (
      <div className="rounded-lg border bg-card">
        <EmptyState
          icon={ActivityIcon}
          title="This speaker sends no metrics"
          description="Its board reports no system.metrics capability, so telemetry is off on it and there is nothing to chart."
        />
      </div>
    )
  }

  const props = { deviceId: device.id, window, timeZone }
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={windowFieldId}>Window</Label>
          <NativeSelect id={windowFieldId} size="sm" value={window.id} onChange={e => setWindowId(e.target.value)}>
            {METRIC_WINDOWS.map(w => <NativeSelectOption key={w.id} value={w.id}>{w.label}</NativeSelectOption>)}
          </NativeSelect>
        </div>
        <p className="text-sm text-muted-foreground">{`The speaker sends a round every ${intervalOf(device)} s; metrics are kept 24 hours.`}</p>
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <CpuCard {...props} />
        <MemoryCard {...props} />
        <TemperatureCard {...props} />
        <StorageCard {...props} />
      </div>
      <Processes deviceId={device.id} />
    </div>
  )
}
