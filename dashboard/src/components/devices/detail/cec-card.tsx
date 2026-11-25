import type { CecDevice } from './cec-state'
import type { CapabilityView } from '@/api/gen'
import { MonitorIcon, MonitorOffIcon, RefreshCwIcon, Volume2Icon, VolumeXIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { sendDeviceCommand } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { CapabilityBadge } from './capability-badge'
import { parseCecState, powerText, typeText, vendorText } from './cec-state'
import { useRefreshDevice } from './use-refresh-device'

function DeviceRow({ device, isTarget, offers, onCommand }: {
  device: CecDevice
  isTarget: boolean
  offers: Set<string>
  onCommand: (name: string, body: Record<string, unknown>) => Promise<void>
}) {
  const titleId = useId()
  const label = device.name || typeText(device.type)
  const vendor = vendorText(device.vendorId)
  const on = device.power === 'on' || device.power === 'to_on'
  const muted = device.audioStatus?.muted === true
  const hasVolume = (device.type === 'tv' || device.type === 'audio_system') && offers.has('volume')
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-2">
      {on
        ? <MonitorIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        : <MonitorOffIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />}
      <div className="flex min-w-0 flex-col" id={titleId}>
        <span className="truncate text-sm font-medium">{label}</span>
        <span className="text-xs text-muted-foreground">
          {`${typeText(device.type)} · address ${device.logicalAddress}${
            vendor
              ? ` · ${vendor}`
              : ''
          } · ${powerText(device.power)}${
            device.audioStatus
              ? ` · volume ${device.audioStatus.volume}%`
              : ''}`}
        </span>
      </div>
      <div className="ml-auto flex items-center gap-1.5">
        {hasVolume && isTarget && (
          <>
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-label={`Volume down on ${label}`}
              disabled={on ? undefined : true}
              onClick={() => void onCommand('volume', { action: 'down' })}
            >
              <span aria-hidden="true">−</span>
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-label={muted
                ? `Unmute ${label}`
                : `Mute ${label}`}
              disabled={on ? undefined : true}
              onClick={() => void onCommand('volume', {
                action: muted ? 'unmute' : 'mute',
              })}
            >
              {muted
                ? <VolumeXIcon aria-hidden="true" className="size-4" />
                : <Volume2Icon aria-hidden="true" className="size-4" />}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-label={`Volume up on ${label}`}
              disabled={on ? undefined : true}
              onClick={() => void onCommand('volume', { action: 'up' })}
            >
              <span aria-hidden="true">+</span>
            </Button>
          </>
        )}
        {offers.has('power') && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => void onCommand('power', {
              action: on ? 'off' : 'on',
            })}
          >
            {on ? 'Turn off' : 'Turn on'}
          </Button>
        )}
      </div>
    </li>
  )
}

// the outputs on the screen's hdmi cec wire: what is connected, what it takes, and the buttons that move it.
// every button is one capability command the device declared (an authenticated caller's press, rule 7), the
// device's detail read again after it settles
export function CecCard({ deviceId, capability }: { deviceId: string, capability: CapabilityView }) {
  const titleId = useId()
  const errorId = useId()
  const api = useApi()
  const refresh = useRefreshDevice(deviceId)
  const state = parseCecState(capability.state)
  const offers = new Set(capability.commands.map(c => c.name))
  const [error, setError] = useState<string>()
  const [pending, setPending] = useState<string>()
  const adapter = state?.adapter
  const systemAudio = state?.systemAudio
  const target = state?.volumeTarget ?? 'tv'
  const devices = state?.devices ?? []

  async function run(name: string, body: Record<string, unknown>) {
    const key = `${name}:${JSON.stringify(body)}`
    setPending(key)
    setError(undefined)
    try {
      await sendDeviceCommand({
        client: api,
        path: { id: deviceId, cap: capability.id, name },
        body,
        throwOnError: true,
      })
    }
    catch (failure) {
      setError(describeError(failure))
    }
    finally {
      setPending(current => current === key
        ? undefined
        : current)
      await refresh()
    }
  }

  function busy(name: string, body: Record<string, unknown>): boolean {
    return pending === `${name}:${JSON.stringify(body)}`
  }

  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 id={titleId} className="font-mono text-sm font-medium">{capability.id}</h3>
        <span className="text-sm text-muted-foreground">{`v${capability.version}`}</span>
        <CapabilityBadge capability={capability} />
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {adapter?.present
          ? (
              <>
                <p className="text-sm text-muted-foreground">
                  {`HDMI ${adapter.physicalAddress ?? 'address unknown'}${
                    adapter.logicalAddress !== undefined
                      ? ` · address ${adapter.logicalAddress}`
                      : ''}`}
                </p>
                {devices.length > 0
                  ? (
                      <ul className="flex flex-col gap-3">
                        {devices.map(d => (
                          <DeviceRow
                            key={d.logicalAddress}
                            device={d}
                            offers={offers}
                            isTarget={(target === 'audio_system') === (d.type === 'audio_system')}
                            onCommand={run}
                          />
                        ))}
                      </ul>
                    )
                  : <p className="text-sm text-muted-foreground">No devices found on the wire. Rescanning asks again.</p>}
                {systemAudio?.available && offers.has('system_audio') && (
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <span className="text-sm">
                      {'Audio through the receiver: '}
                      <span className="font-medium">
                        {systemAudio.enabled === 'on'
                          ? 'on'
                          : systemAudio.enabled === 'off'
                            ? 'off'
                            : 'unknown'}
                      </span>
                    </span>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy('system_audio', { enabled: systemAudio.enabled !== 'on' })}
                      onClick={() => void run('system_audio', { enabled: systemAudio.enabled !== 'on' })}
                    >
                      {systemAudio.enabled === 'on'
                        ? 'Use TV speakers'
                        : 'Use the receiver'}
                    </Button>
                  </div>
                )}
              </>
            )
          : <p className="text-sm text-muted-foreground">No HDMI CEC adapter on this device.</p>}
        <div className="flex flex-wrap items-center gap-2">
          {offers.has('refresh') && (
            <Button type="button" variant="outline" size="sm" onClick={() => void run('refresh', {})}>
              <RefreshCwIcon
                aria-hidden="true"
                className={busy('refresh', {})
                  ? 'animate-spin'
                  : undefined}
              />
              Rescan
            </Button>
          )}
          {error && <p id={errorId} role="alert" className="text-xs text-destructive">{`The command failed. ${error}`}</p>}
        </div>
      </CardContent>
    </Card>
  )
}
