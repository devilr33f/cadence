import type { Generations } from './apply-state'
import { applyState } from './apply-state'
import { StatusBadge } from './status-badge'

// desired vs applied: a change travels from the generation in force to the desired one, and a failure stays in view
export function ApplyBadge({ appearance = 'pill', ...generations }: Generations & { appearance?: 'inline' | 'pill' }) {
  const { desiredGen, appliedGen, appliedError } = generations
  switch (applyState(generations)) {
    case 'applied':
      return <StatusBadge tone="success" label="Applied" detail={`gen ${appliedGen}`} appearance={appearance} />
    case 'pending':
      return <StatusBadge tone="info" label="Pending" detail={`gen ${appliedGen} → ${desiredGen}`} appearance={appearance} />
    case 'failed':
      return (
        <StatusBadge
          tone="danger"
          label="Failed"
          appearance={appearance}
          detail={(
            <>
              <s>{`gen ${desiredGen}`}</s>
              {' '}
              <code className="font-mono text-xs">{appliedError}</code>
            </>
          )}
        />
      )
  }
}
