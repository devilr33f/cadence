import type { GroupView } from '@/api/gen'
import { StatusBadge } from '@/components/data/status-badge'

// streaming to a member, ready with a leader, or with no member that can lead (none online with media.group)
export function GroupActivity({ group, appearance }: { group: GroupView, appearance?: 'inline' | 'pill' }) {
  if (group.active)
    return <StatusBadge tone="success" label="Active" appearance={appearance} />
  if (group.leaderId === '')
    return <StatusBadge tone="neutral" label="No leader" glyph="dashed" appearance={appearance} />
  return <StatusBadge tone="neutral" label="Idle" appearance={appearance} />
}
