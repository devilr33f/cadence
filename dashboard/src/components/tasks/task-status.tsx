import type { TaskJson } from '@/api/gen'
import { StatusBadge } from '@/components/data/status-badge'
import { STATUS } from './task-words'

export function TaskStatus({ task, appearance }: { task: Pick<TaskJson, 'status' | 'errorCode'>, appearance?: 'inline' | 'pill' }) {
  const known = STATUS[task.status]
  return (
    <StatusBadge
      tone={known?.tone ?? 'neutral'}
      label={known?.label ?? task.status}
      appearance={appearance}
      detail={task.status === 'failed' && task.errorCode
        ? <span className="font-mono text-xs">{task.errorCode}</span>
        : undefined}
    />
  )
}
