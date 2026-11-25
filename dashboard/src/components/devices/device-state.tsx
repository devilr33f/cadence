import type { DeviceView } from '@/api/gen'
import { DeviceStatus } from '@/components/data/device-status'

// a pending device is enrolling; an enrolled one is online or offline, as the server's link says
export function DeviceStateBadge({ device, appearance }: {
  device: Pick<DeviceView, 'status' | 'online'>
  appearance?: 'inline' | 'pill'
}) {
  if (device.status !== 'enrolled')
    return <DeviceStatus status="enrolling" appearance={appearance} />
  return <DeviceStatus status={device.online ? 'online' : 'offline'} appearance={appearance} />
}
