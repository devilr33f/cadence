import type { ReactNode } from 'react'
import type { DeviceView } from '@/api/gen'
import type { LogFilter, LogFilterField, LogLevel } from '@/lib/logs'
import { useId } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

const LEVELS: { value: LogLevel, label: string }[] = [
  { value: '', label: 'Any' },
  { value: 'info', label: 'Info' },
  { value: 'warn', label: 'Warn' },
  { value: 'error', label: 'Error' },
]

const CONTROL = 'h-8 text-sm'

function Field({ label, error, children }: {
  label: string
  error?: string
  children: (props: { 'id': string, 'aria-invalid'?: true, 'aria-describedby'?: string }) => ReactNode
}) {
  const id = useId()
  const errorId = `${id}-error`
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children({
        id,
        ...(error && { 'aria-invalid': true, 'aria-describedby': errorId }),
      })}
      {error && <p id={errorId} role="alert" className="text-sm text-destructive">{error}</p>}
    </div>
  )
}

// the row of filters above the lines; the range is the household's wall time, and a tail ignores it (its lines are new)
export function LogFilters({ filter, onChange, errors, devices, zone, live }: {
  filter: LogFilter
  onChange: (filter: LogFilter) => void
  errors: Partial<Record<LogFilterField, string>>
  devices: readonly DeviceView[]
  zone: string | null
  live: boolean
}) {
  const set = <K extends LogFilterField>(key: K, value: LogFilter[K]) => onChange({ ...filter, [key]: value })
  const searchWithoutSince = (filter.q.trim() !== '' || filter.level !== '') && filter.since === '' && !live
  return (
    <div className="flex flex-col gap-2">
      <div role="group" aria-label="Filter the lines" className="flex flex-wrap items-start gap-3">
        <Field label="Speaker" error={errors.device}>
          {props => (
            <NativeSelect {...props} size="sm" value={filter.device} onChange={e => set('device', e.target.value)}>
              <NativeSelectOption value="">Every speaker and the server</NativeSelectOption>
              {devices.map(d => <NativeSelectOption key={d.id} value={d.id}>{d.name}</NativeSelectOption>)}
            </NativeSelect>
          )}
        </Field>
        <Field label="Source" error={errors.source}>
          {props => (
            <Input {...props} className={`${CONTROL} w-40 font-mono`} placeholder="cadenced, kmsg, server" spellCheck={false} value={filter.source} onChange={e => set('source', e.target.value)} />
          )}
        </Field>
        <Field label="Lowest level" error={errors.level}>
          {props => (
            <NativeSelect {...props} size="sm" value={filter.level} onChange={e => set('level', e.target.value as LogLevel)}>
              {LEVELS.map(l => <NativeSelectOption key={l.value} value={l.value}>{l.label}</NativeSelectOption>)}
            </NativeSelect>
          )}
        </Field>
        <Field label="Contains" error={errors.q}>
          {props => (
            <Input {...props} type="search" className={`${CONTROL} w-48`} spellCheck={false} value={filter.q} onChange={e => set('q', e.target.value)} />
          )}
        </Field>
        <Field label="From" error={errors.since}>
          {props => (
            <Input {...props} type="datetime-local" className={`${CONTROL} w-52`} disabled={live || zone === null} value={filter.since} onChange={e => set('since', e.target.value)} />
          )}
        </Field>
        <Field label="Until" error={errors.until}>
          {props => (
            <Input {...props} type="datetime-local" className={`${CONTROL} w-52`} disabled={live || zone === null} value={filter.until} onChange={e => set('until', e.target.value)} />
          )}
        </Field>
      </div>
      <p className="flex flex-wrap gap-x-1 text-sm text-muted-foreground">
        <span>
          {live
            ? 'A tail shows new lines, so From and Until wait for it to stop.'
            : zone && `Times are ${zone}'s.`}
        </span>
        {searchWithoutSince && <span>A search by text or level reads the last day unless From is set.</span>}
      </p>
    </div>
  )
}
