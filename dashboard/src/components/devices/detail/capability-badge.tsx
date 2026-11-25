import type { CapabilityView } from '@/api/gen'
import { ApplyBadge } from '@/components/data/apply-badge'
import { StatusBadge } from '@/components/data/status-badge'

// a capability never configured (generation 0) runs on the device's own defaults: nothing to apply. one the device
// says has no hardware now (a screen's hdmi unplugged) is also marked: kept and configurable, its commands refused
export function CapabilityBadge({ capability }: { capability: CapabilityView }) {
  const { desiredGen, appliedGen, appliedError, available } = capability
  const apply = desiredGen === 0 && appliedGen === 0 && !appliedError
    ? <StatusBadge tone="neutral" glyph="ring" label="Device defaults" appearance="pill" />
    : <ApplyBadge desiredGen={desiredGen} appliedGen={appliedGen} appliedError={appliedError} />
  if (available !== false)
    return apply
  return (
    <>
      {apply}
      <StatusBadge tone="warning" label="Unavailable" detail="hardware absent" appearance="pill" />
    </>
  )
}
