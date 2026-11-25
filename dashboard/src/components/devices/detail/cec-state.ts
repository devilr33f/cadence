// display.cec's state as the device reports it, and the words the card shows it in

// one device on the wire
export interface CecDevice {
  logicalAddress: number
  type: string
  physicalAddress?: string
  vendorId?: number
  name?: string
  power: string
  audioStatus?: { volume: number, muted: boolean }
  supports?: string[]
}

export interface CecState {
  adapter?: { present: boolean, physicalAddress?: string, logicalAddress?: number, deviceType?: string }
  systemAudio?: { available: boolean, enabled: string }
  volumeTarget?: string
  devices?: CecDevice[]
}

export function parseCecState(state: unknown): CecState | undefined {
  if (state === null || typeof state !== 'object')
    return undefined
  return state as CecState
}

const TYPE_TEXT: Record<string, string> = {
  tv: 'TV',
  recording: 'Recorder',
  tuner: 'Tuner',
  playback: 'Player',
  audio_system: 'Receiver',
  switch: 'Switch',
  processor: 'Processor',
  reserved: 'Reserved',
  free: 'Other',
}

// the vendor ids the wire carries most often (cec's registered ranges), shown as names; anything else keeps its hex
const VENDORS: Record<number, string> = {
  0x0000F0: 'Samsung',
  0x00E091: 'LG',
  0x080046: 'Sony',
  0x008045: 'Panasonic',
  0x009000: 'Philips',
  0x0005C5: 'TCL',
  0x0609CD: 'Xiaomi',
  0x000925: 'Pulse-Eight',
}

export function typeText(type: string): string {
  return TYPE_TEXT[type] ?? 'Device'
}

export function vendorText(id: number | undefined): string | undefined {
  if (id === undefined || id === 0)
    return undefined
  return VENDORS[id] ?? `vendor ${id.toString(16).padStart(6, '0')}`
}

export function powerText(power: string): string {
  switch (power) {
    case 'on': return 'On'
    case 'standby': return 'Standby'
    case 'to_on': return 'Waking'
    case 'to_off': return 'Going to standby'
    default: return 'Power unknown'
  }
}
