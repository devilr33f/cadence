import type { DeviceView } from '@/api/gen'
import type { TraceFilter } from '@/lib/traces/filter'
import { useId } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'

function Toggle({ label, checked, onChange }: { label: string, checked: boolean, onChange: (on: boolean) => void }) {
  const id = useId()
  return (
    <label className="flex h-8 items-center gap-2 rounded-md px-1 text-sm font-medium">
      <Switch aria-labelledby={id} checked={checked} onCheckedChange={onChange} />
      <span id={id}>{label}</span>
    </label>
  )
}

// the row above the list: the speaker is read on the server, the rest narrow what the page holds
export function TraceFilters({ device, onDevice, devices, filter, onChange }: {
  device: string
  onDevice: (device: string) => void
  devices: readonly DeviceView[]
  filter: TraceFilter
  onChange: (filter: TraceFilter) => void
}) {
  const deviceId = useId()
  const nameId = useId()
  return (
    <div role="group" aria-label="Filter the traces" className="flex flex-wrap items-end gap-3">
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={deviceId}>Speaker</Label>
        <NativeSelect id={deviceId} size="sm" value={device} onChange={e => onDevice(e.target.value)}>
          <NativeSelectOption value="">Every speaker and the server</NativeSelectOption>
          {devices.map(d => <NativeSelectOption key={d.id} value={d.id}>{d.name}</NativeSelectOption>)}
          {device !== '' && !devices.some(d => d.id === device) && <NativeSelectOption value={device}>{device}</NativeSelectOption>}
        </NativeSelect>
      </div>
      <div className="flex min-w-0 flex-col gap-1.5">
        <Label htmlFor={nameId}>Name</Label>
        <Input id={nameId} type="search" className="h-8 w-48 font-mono text-sm" spellCheck={false} placeholder="voice.turn" value={filter.name} onChange={e => onChange({ ...filter, name: e.target.value })} />
      </div>
      <Toggle label="Running only" checked={filter.liveOnly} onChange={liveOnly => onChange({ ...filter, liveOnly })} />
      <Toggle label="Errors only" checked={filter.errorsOnly} onChange={errorsOnly => onChange({ ...filter, errorsOnly })} />
    </div>
  )
}
