import type { CapabilityView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { putDeviceConfig } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { KeyValueList } from '@/components/data/key-value-list'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { buttonVariants } from '@/components/ui/button-variants'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { readyWords, wakeWordsQueryOptions } from '@/lib/wakewords'
import { CapabilityBadge } from './capability-badge'
import { useRefreshDevice } from './use-refresh-device'

interface Desired {
  enabled: boolean
  modelId?: string
  sha256?: string
  sensitivity?: number
}

interface State {
  state?: string
  word?: string
  detections?: number
  code?: string
}

function record(v: unknown): Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
    ? v as Record<string, unknown>
    : {}
}

function parseDesired(v: unknown): Desired {
  const r = record(v)
  return {
    enabled: r.enabled === true,
    modelId: typeof r.modelId === 'string'
      ? r.modelId
      : undefined,
    sha256: typeof r.sha256 === 'string'
      ? r.sha256
      : undefined,
    sensitivity: typeof r.sensitivity === 'number'
      ? r.sensitivity
      : undefined,
  }
}

function parseState(v: unknown): State {
  const r = record(v)
  return {
    state: typeof r.state === 'string'
      ? r.state
      : undefined,
    word: typeof r.word === 'string'
      ? r.word
      : undefined,
    detections: typeof r.detections === 'number'
      ? r.detections
      : undefined,
    code: typeof r.code === 'string'
      ? r.code
      : undefined,
  }
}

function StateBadge({ state }: { state: State }) {
  switch (state.state) {
    case 'listening':
      return <StatusBadge tone="success" label="Listening" detail={state.word} />
    case 'loading':
      return <StatusBadge tone="info" label="Loading the model" />
    case 'failed':
      return <StatusBadge tone="danger" label="Failed" detail={state.code} />
    default:
      return <StatusBadge tone="neutral" label="Off" />
  }
}

// wake.custom: a switch, the word from the wake words the server holds, and a sensitivity; saved as the capability's
// config with the word's model id and sha256, which the device fetches and checks
export function WakeWordCard({ deviceId, capability }: { deviceId: string, capability: CapabilityView }) {
  const api = useApi()
  const refresh = useRefreshDevice(deviceId)
  const words = useQuery(wakeWordsQueryOptions(api))
  const desired = parseDesired(capability.desired)
  const state = parseState(capability.state)
  const ready = readyWords(words.data)
  const [enabled, setEnabled] = useState(desired.enabled)
  const [modelId, setModelId] = useState(desired.modelId ?? '')
  const [sensitivity, setSensitivity] = useState(desired.sensitivity ?? 0.5)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const titleId = useId()
  const switchId = useId()
  const wordId = useId()
  const sensitivityId = useId()
  // a word the config names that is gone (deleted) shows as the first ready one, which a save then sets
  const chosen = ready.find(w => w.id === modelId) ?? ready[0]

  async function save() {
    if (pending)
      return
    if (enabled && !chosen) {
      setError('Choose a word first.')
      return
    }
    setPending(true)
    setError(undefined)
    try {
      const body: Record<string, unknown> = enabled
        ? {
            enabled: true,
            modelId: chosen!.id,
            sha256: chosen!.sha256,
            sensitivity,
          }
        : { enabled: false }
      await putDeviceConfig({
        client: api,
        path: { id: deviceId, cap: capability.id },
        body,
        throwOnError: true,
      })
      await refresh()
    }
    catch (failure) {
      setError(describeError(failure))
    }
    finally {
      setPending(false)
    }
  }

  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 id={titleId} className="font-mono text-sm font-semibold">{capability.id}</h3>
        <CapabilityBadge capability={capability} />
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <KeyValueList items={[
          { label: 'State', value: <StateBadge state={state} /> },
          { label: 'Wakes heard', value: state.detections ?? 0 },
        ]}
        />
        <div className="flex items-center gap-3">
          <Switch id={switchId} checked={enabled} onCheckedChange={setEnabled} />
          <Label htmlFor={switchId}>Wake on a word of your own</Label>
        </div>
        {ready.length === 0
          ? (
              <p className="text-sm text-muted-foreground">
                No word is ready yet:
                {' '}
                <Link to="/wakewords" className={buttonVariants({ variant: 'link', className: 'h-auto p-0' })}>train or upload one</Link>
                .
              </p>
            )
          : (
              <div className="grid max-w-md gap-4 sm:grid-cols-2">
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={wordId}>Word</Label>
                  <NativeSelect id={wordId} value={chosen?.id ?? ''} onChange={e => setModelId(e.target.value)}>
                    {ready.map(w => <NativeSelectOption key={w.id} value={w.id}>{w.word}</NativeSelectOption>)}
                  </NativeSelect>
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={sensitivityId}>{`Sensitivity ${Math.round(sensitivity * 100)}%`}</Label>
                  <input
                    id={sensitivityId}
                    type="range"
                    min={0}
                    max={1}
                    step={0.05}
                    value={sensitivity}
                    className="h-9 accent-foreground"
                    onChange={e => setSensitivity(Number(e.target.value))}
                  />
                </div>
              </div>
            )}
        <p className="max-w-[65ch] text-sm text-muted-foreground">
          Higher wakes more easily and more often by mistake. The speaker listens only while no request runs; nothing it
          hears is kept.
        </p>
        <div className="flex flex-col items-start gap-2">
          <Button type="button" variant="secondary" aria-disabled={pending || undefined} onClick={() => void save()}>
            Save the wake word
          </Button>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        </div>
      </CardContent>
    </Card>
  )
}
