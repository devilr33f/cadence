import type { FormEvent, ReactNode } from 'react'
import type { WiFiNetwork, WiFiState } from './wifi-state'
import type { CapabilityView } from '@/api/gen'
import type { KeyValue } from '@/components/data/key-value-list'
import type { Tone } from '@/components/data/tone'
import { LoaderCircleIcon, RefreshCwIcon, WifiIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { configureDeviceCapability, putDeviceConfig, sendDeviceCommand } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { KeyValueList } from '@/components/data/key-value-list'
import { StatusBadge } from '@/components/data/status-badge'
import { SecretInput } from '@/components/forms/secret-input'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { CapabilityBadge } from './capability-badge'
import { useRefreshDevice } from './use-refresh-device'
import { bandText, linkText, parseScan, parseWiFiState, pskProblem, securityOf, signalText, ssidProblem } from './wifi-state'

function Part({ title, children }: { title: string, children: ReactNode }) {
  const titleId = useId()
  return (
    <div role="group" aria-labelledby={titleId} className="flex flex-col gap-2">
      <h4 id={titleId} className="text-sm font-semibold">{title}</h4>
      {children}
    </div>
  )
}

const REASONS: Record<string, string> = {
  no_ip: 'it never got an address (a wrong password, or out of range)',
  no_server: 'it could not reach the server over it',
}

// what the capability is doing, in words: a switch under way, one rolled back and why, a missing password
function ManagedStatus({ state }: { state: WiFiState }) {
  let tone: Tone = 'neutral'
  let label = 'Not managed'
  let detail: string | undefined
  if (state.code === 'switching') {
    tone = 'info'
    label = `Switching to ${state.target ?? 'the network'}`
    detail = 'back on the previous network within about two minutes if the server is not reached'
  }
  else if (state.code === 'rolled_back') {
    tone = 'warning'
    label = `Rolled back from ${state.target ?? 'the network'}`
    detail = REASONS[state.reason ?? ''] ?? state.reason
  }
  else if (state.code === 'psk_missing') {
    tone = 'warning'
    label = 'No password set'
  }
  else if (state.managed) {
    tone = 'success'
    label = state.target
      ? `Managed: ${state.target}`
      : 'Managed'
  }
  return <StatusBadge tone={tone} label={label} detail={detail} />
}

function stateItems(state: WiFiState): KeyValue[] {
  const items: KeyValue[] = [
    { label: 'Network', value: state.ssid || linkText(state.state) },
    {
      label: 'Link',
      value: linkText(state.state),
    },
  ]
  const band = bandText(state.freqMHz)
  if (band)
    items.push({ label: 'Band', value: band })
  if (state.ip)
    items.push({ label: 'Address', value: state.ip, mono: true })
  items.push({ label: 'Cadence', value: <ManagedStatus state={state} /> })
  return items
}

function NetworkRow({ network, onChoose }: { network: WiFiNetwork, onChoose: (ssid: string) => void }) {
  const security = securityOf(network.security)
  const name = network.ssid || 'Hidden network'
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5">
      <WifiIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
      <div className="flex min-w-0 flex-col">
        <span className="truncate text-sm font-medium">{name}</span>
        <span className="text-xs text-muted-foreground">
          {`${signalText(network.signalDbm)} · ${bandText(network.freqMHz) ?? 'band unknown'} · ${security.label}${
            security.supported
              ? ''
              : ' (not supported)'}`}
        </span>
      </div>
      {security.supported && network.ssid !== '' && (
        <Button type="button" variant="ghost" size="sm" className="ml-auto" onClick={() => onChoose(network.ssid)}>
          {`Choose ${name}`}
        </Button>
      )}
    </li>
  )
}

// network.wifi: the link now, a scan of what is around, and a connect that sends the network's name and its password
// as one generation (configure-device-capability), so the device never sees the one without the other. the password
// goes from an uncontrolled field to that one request: never in state, a cache, a url or an attribute
export function WiFiCard({ deviceId, capability }: { deviceId: string, capability: CapabilityView }) {
  const titleId = useId()
  const formId = useId()
  const api = useApi()
  const refresh = useRefreshDevice(deviceId)
  const state = parseWiFiState(capability.state)
  const offers = new Set(capability.commands.map(c => c.name))
  const ssidRef = useRef<HTMLInputElement>(null)
  const pskRef = useRef<HTMLInputElement>(null)
  const [ssid, setSsid] = useState('')
  const [networks, setNetworks] = useState<WiFiNetwork[]>()
  const [scanning, setScanning] = useState(false)
  const [scanError, setScanError] = useState<string>()
  const [connecting, setConnecting] = useState(false)
  const [fieldError, setFieldError] = useState<{ field: 'ssid' | 'psk', message: string }>()
  const [connectError, setConnectError] = useState<string>()
  const [sent, setSent] = useState<string>()
  const [stopping, setStopping] = useState(false)
  const [stopError, setStopError] = useState<string>()

  async function scan() {
    if (scanning)
      return
    setScanning(true)
    setScanError(undefined)
    try {
      const { data } = await sendDeviceCommand({
        client: api,
        path: { id: deviceId, cap: capability.id, name: 'scan' },
        body: {},
        throwOnError: true,
      })
      setNetworks(parseScan((data as { result?: unknown } | undefined)?.result))
    }
    catch (failure) {
      setScanError(describeError(failure))
    }
    finally {
      setScanning(false)
    }
  }

  function choose(name: string) {
    setSsid(name)
    setFieldError(undefined)
    pskRef.current?.focus()
  }

  async function connect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const key = new FormData(form).get('key')
    // the password leaves the field at once, whatever happens next
    if (pskRef.current)
      pskRef.current.value = ''
    if (connecting)
      return
    const name = ssid.trim()
    const ssidIssue = ssidProblem(name)
    if (ssidIssue) {
      setFieldError({ field: 'ssid', message: ssidIssue })
      ssidRef.current?.focus()
      return
    }
    const pskIssue = typeof key === 'string'
      ? pskProblem(key)
      : 'Type the password.'
    if (pskIssue || typeof key !== 'string') {
      setFieldError({ field: 'psk', message: pskIssue ?? 'Type the password.' })
      pskRef.current?.focus()
      return
    }
    setFieldError(undefined)
    setConnectError(undefined)
    setSent(undefined)
    setConnecting(true)
    try {
      await configureDeviceCapability({
        client: api,
        path: { id: deviceId, cap: capability.id },
        body: {
          config: { enabled: true, ssid: name },
          secrets: { psk: key },
        },
        throwOnError: true,
      })
      setSent(name)
      await refresh()
    }
    catch (failure) {
      setConnectError(describeError(failure))
    }
    finally {
      setConnecting(false)
    }
  }

  async function stopManaging() {
    if (stopping)
      return
    setStopping(true)
    setStopError(undefined)
    try {
      await putDeviceConfig({
        client: api,
        path: { id: deviceId, cap: capability.id },
        body: { enabled: false },
        throwOnError: true,
      })
      await refresh()
    }
    catch (failure) {
      setStopError(describeError(failure))
    }
    finally {
      setStopping(false)
    }
  }

  const ssidError = fieldError?.field === 'ssid'
    ? fieldError.message
    : undefined
  const pskError = fieldError?.field === 'psk'
    ? fieldError.message
    : undefined

  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 id={titleId} className="font-mono text-sm font-medium">{capability.id}</h3>
        <span className="text-sm text-muted-foreground">{`v${capability.version}`}</span>
        <CapabilityBadge capability={capability} />
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <Part title="Wi-Fi now">
          {state
            ? <KeyValueList items={stateItems(state)} />
            : <p className="text-sm text-muted-foreground">No state reported.</p>}
          {state?.managed && (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-disabled={stopping ? true : undefined}
                onClick={() => void stopManaging()}
              >
                Stop managing the Wi-Fi
              </Button>
              {stopError && <p role="alert" className="text-xs text-destructive">{`Not changed. ${stopError}`}</p>}
            </div>
          )}
        </Part>
        {offers.has('scan') && (
          <Part title="Networks nearby">
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-disabled={scanning ? true : undefined}
                onClick={() => void scan()}
              >
                <RefreshCwIcon aria-hidden="true" className={scanning ? 'motion-safe:animate-spin' : undefined} />
                {scanning ? 'Scanning…' : 'Scan'}
              </Button>
              {scanError && <p role="alert" className="text-xs text-destructive">{`The scan failed. ${scanError}`}</p>}
            </div>
            {networks && (networks.length > 0
              ? (
                  <ul aria-label="Networks found" className="flex flex-col divide-y">
                    {networks.map(n => <NetworkRow key={`${n.bssid}-${n.ssid}`} network={n} onChoose={choose} />)}
                  </ul>
                )
              : <p className="text-sm text-muted-foreground">No networks found. Scanning again may find more.</p>)}
          </Part>
        )}
        <Part title="Connect to a network">
          <p className="text-sm text-muted-foreground">
            The speaker keeps its current network saved. If it gets no address, or cannot reach the server over the new
            one, it goes back to the current network within about two minutes.
          </p>
          <form method="post" autoComplete="off" onSubmit={e => void connect(e)} className="flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${formId}-ssid`}>Network name</Label>
              <Input
                ref={ssidRef}
                id={`${formId}-ssid`}
                name="ssid"
                autoComplete="off"
                spellCheck={false}
                className="w-full sm:w-72"
                value={ssid}
                onChange={e => setSsid(e.target.value)}
                aria-invalid={ssidError ? true : undefined}
                aria-describedby={ssidError
                  ? `${formId}-ssid-error`
                  : undefined}
              />
              {ssidError && <p id={`${formId}-ssid-error`} role="alert" className="text-xs text-destructive">{ssidError}</p>}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${formId}-key`}>Password</Label>
              <SecretInput
                ref={pskRef}
                id={`${formId}-key`}
                name="key"
                className="w-full sm:w-72"
                aria-invalid={pskError ? true : undefined}
                aria-describedby={pskError
                  ? `${formId}-key-error`
                  : undefined}
              />
              {pskError && <p id={`${formId}-key-error`} role="alert" className="text-xs text-destructive">{pskError}</p>}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button type="submit" variant="secondary" aria-disabled={connecting ? true : undefined}>
                {connecting && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
                Connect
              </Button>
              {connectError && <p role="alert" className="text-xs text-destructive">{`Not sent. ${connectError}`}</p>}
              {sent && !connectError && (
                <p role="status" className="text-xs text-muted-foreground">
                  {`Sent. The speaker switches to ${sent} and may be offline for a moment.`}
                </p>
              )}
            </div>
          </form>
        </Part>
      </CardContent>
    </Card>
  )
}
