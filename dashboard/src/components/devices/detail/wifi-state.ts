// network.wifi's state and scan as the device reports them, and the words the card shows them in

export interface WiFiState {
  state: string
  managed: boolean
  ssid?: string
  bssid?: string
  freqMHz?: number
  ip?: string
  // the network the config names, or the one a switch heads for
  target?: string
  code?: string
  reason?: string
}

export interface WiFiNetwork {
  ssid: string
  bssid: string
  freqMHz: number
  signalDbm: number
  security: string
}

export function parseWiFiState(state: unknown): WiFiState | undefined {
  if (state === null || typeof state !== 'object' || typeof (state as WiFiState).state !== 'string')
    return undefined
  return state as WiFiState
}

export function parseScan(result: unknown): WiFiNetwork[] {
  if (result === null || typeof result !== 'object')
    return []
  const networks = (result as { networks?: unknown }).networks
  return Array.isArray(networks)
    ? networks.filter((n): n is WiFiNetwork => n !== null && typeof n === 'object' && typeof n.ssid === 'string')
    : []
}

export function linkText(state: string): string {
  switch (state) {
    case 'completed': return 'Connected'
    case 'associating': return 'Associating'
    case 'scanning': return 'Scanning'
    case 'disconnected': return 'Disconnected'
    case 'inactive': return 'Inactive'
    case 'unsupported': return 'Not reachable on this device'
    default: return 'Unknown'
  }
}

export function bandText(freqMHz: number | undefined): string | undefined {
  if (!freqMHz)
    return undefined
  const band = freqMHz >= 5925
    ? '6 GHz'
    : freqMHz >= 4900
      ? '5 GHz'
      : '2.4 GHz'
  return `${band} · ${freqMHz} MHz`
}

// the device joins wpa-psk networks alone (wpa2, and wpa3's transition mode beside it); anything else is listed, not
// offered
export function securityOf(flags: string): { label: string, supported: boolean } {
  const psk = flags.includes('PSK')
  if (psk) {
    return {
      label: flags.includes('SAE')
        ? 'WPA2/WPA3'
        : flags.includes('WPA2')
          ? 'WPA2'
          : 'WPA',
      supported: true,
    }
  }
  if (flags.includes('SAE'))
    return { label: 'WPA3 only', supported: false }
  if (flags.includes('EAP'))
    return { label: 'Enterprise', supported: false }
  if (flags.includes('WEP'))
    return { label: 'WEP', supported: false }
  return { label: 'Open', supported: false }
}

export function signalText(dbm: number): string {
  const strength = dbm >= -55
    ? 'strong'
    : dbm >= -70
      ? 'good'
      : 'weak'
  return `${dbm} dBm, ${strength}`
}

// a password as the device takes it: 8 to 63 bytes (wpa's passphrase)
export function pskProblem(value: string): string | undefined {
  const bytes = new TextEncoder().encode(value).length
  if (bytes < 8)
    return 'The password needs 8 characters at least.'
  if (bytes > 63)
    return 'The password takes 63 characters at most.'
  return undefined
}

export function ssidProblem(value: string): string | undefined {
  if (value === '')
    return 'Name the network.'
  if (new TextEncoder().encode(value).length > 32)
    return 'A network name takes 32 bytes at most.'
  return undefined
}
