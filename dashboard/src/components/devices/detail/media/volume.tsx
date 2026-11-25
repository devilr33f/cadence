import type { CapabilityView } from '@/api/gen'
import { Volume1Icon, Volume2Icon, VolumeXIcon } from 'lucide-react'
import { useId } from 'react'
import { sendDeviceCommand } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { useRefreshDevice } from '../use-refresh-device'
import { useDraft } from './use-draft'

// the level the device reports in its audio.output state, when it reports one
function levelOf(output: CapabilityView | undefined): number | undefined {
  const state = output?.state as { level?: unknown } | null | undefined
  return typeof state?.level === 'number'
    ? state.level
    : undefined
}

// the device's level, set with audio.output's set_level: the dashboard is an authenticated caller, so it may raise it
// (rule 7). the final value of a drag is sent, then the device's detail read again
export function Volume({ deviceId, output }: { deviceId: string, output: CapabilityView | undefined }) {
  const api = useApi()
  const refresh = useRefreshDevice(deviceId)
  const errorId = useId()
  const level = levelOf(output)
  const volume = useDraft(async (value) => {
    try {
      await sendDeviceCommand({
        client: api,
        path: { id: deviceId, cap: 'audio.output', name: 'set_level' },
        body: { level: value },
        throwOnError: true,
      })
    }
    finally {
      await refresh()
    }
  })
  if (level === undefined || !output?.commands.some(c => c.name === 'set_level'))
    return null
  const shown = volume.draft ?? level
  const Icon = shown === 0
    ? VolumeXIcon
    : shown < 50
      ? Volume1Icon
      : Volume2Icon
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <Icon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        <input
          type="range"
          aria-label="Volume"
          min={0}
          max={100}
          step={1}
          value={shown}
          aria-valuetext={`${shown}%`}
          aria-invalid={volume.error ? true : undefined}
          aria-describedby={volume.error ? errorId : undefined}
          onChange={e => volume.change(Number(e.target.value))}
          className="h-1 min-w-0 flex-1 cursor-pointer accent-foreground"
        />
        <span aria-hidden="true" className="w-8 text-right text-xs text-muted-foreground tabular-nums">{shown}</span>
      </div>
      {volume.error && <p id={errorId} role="alert" className="text-xs text-destructive">{`The volume could not be set. ${volume.error}`}</p>}
    </div>
  )
}
