import type { TurnFailure as TurnFailureData } from '@/api/gen'
import type { KeyValue } from '@/components/data/key-value-list'
import { KeyValueList } from '@/components/data/key-value-list'
import { StatusBadge } from '@/components/data/status-badge'
import { failureCodeLine, failureReason, failureStage } from './turn-failure-text'

export function TurnFailure({ failure }: { failure: TurnFailureData }) {
  const items: KeyValue[] = [
    {
      label: 'Stage',
      value: failureStage(failure),
    },
  ]
  if (failure.step) {
    items.push({
      label: 'Step',
      value: failure.step,
    })
  }
  if (failure.tool) {
    items.push({
      label: 'Tool',
      value: failure.tool,
      mono: true,
    })
  }
  if (failure.provider) {
    items.push({
      label: 'Provider',
      value: failure.provider,
      mono: true,
    })
  }
  if (failure.model) {
    items.push({
      label: 'Model',
      value: failure.model,
      mono: true,
    })
  }
  return (
    <section aria-label="Turn failed" className="mt-5 flex flex-col gap-4 rounded-lg border border-destructive/40 bg-destructive-surface p-4">
      <StatusBadge tone="danger" label="Turn failed" detail={failure.code} />
      <p className="text-base font-medium break-words">{failureReason(failure)}</p>
      <KeyValueList items={items} />
      <code className="font-mono text-xs break-words text-destructive">{failureCodeLine(failure)}</code>
    </section>
  )
}
