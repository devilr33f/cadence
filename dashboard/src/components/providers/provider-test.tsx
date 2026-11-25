import type { ProviderTest, TestOutcome } from './use-provider-tests'
import type { TestResult } from '@/api/gen'
import type { Tone } from '@/components/data/tone'
import { LoaderCircleIcon } from 'lucide-react'
import { ApiError } from '@/api/error'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { useSecondsLeft } from '@/hooks/use-now'

type TestCode = NonNullable<TestResult['code']>

// what each of the server's codes says; the code itself follows in mono, never upstream text
const CODE_TEXT: Record<TestCode, { label: string, tone: Tone }> = {
  auth: { label: 'Credential refused', tone: 'danger' },
  unreachable: { label: 'Unreachable', tone: 'danger' },
  timeout: { label: 'Timed out', tone: 'danger' },
  bad_response: { label: 'Unexpected answer', tone: 'danger' },
  unsupported: { label: 'Cannot be tested', tone: 'neutral' },
  rate_limited: { label: 'Upstream rate limited', tone: 'warning' },
  build_failed: { label: 'Does not build', tone: 'danger' },
}

function codeText(code: string): { label: string, tone: Tone } {
  return Object.hasOwn(CODE_TEXT, code)
    ? CODE_TEXT[code as TestCode]
    : { label: 'Failed', tone: 'danger' }
}

function OutcomeBadge({ outcome }: { outcome: TestOutcome }) {
  if ('error' in outcome) {
    // the test request itself was refused or got no answer: its status and code, nothing more
    const { error } = outcome
    return (
      <StatusBadge
        tone="danger"
        label="Not tested"
        detail={error instanceof ApiError
          ? <code className="font-mono text-xs">{`${error.status} ${error.error}`}</code>
          : 'no answer'}
      />
    )
  }
  const { result } = outcome
  if (result.ok)
    return <StatusBadge tone="success" label="OK" detail={`${result.ms} ms`} />
  const code = result.code ?? 'bad_response'
  const { label, tone } = codeText(code)
  return <StatusBadge tone={tone} label={label} detail={<code className="font-mono text-xs">{code}</code>} />
}

// the row's last test on this page; a polite live region, so the outcome is read out where the button was pressed
export function ProviderTestBadge({ test }: { test?: ProviderTest }) {
  const left = useSecondsLeft(test?.until)
  let badge = null
  if (test?.running) {
    badge = <StatusBadge tone="info" label="Testing" />
  }
  else if (test?.wait !== undefined && left > 0) {
    // the countdown is seen; read out once, as it started, not each second. the clock may lag its start by a tick
    badge = (
      <StatusBadge
        tone="neutral"
        label="Wait"
        detail={(
          <>
            <span aria-hidden="true">{`${Math.min(left, test.wait)} s`}</span>
            <span className="sr-only">{`${test.wait} s before another test`}</span>
          </>
        )}
      />
    )
  }
  else if (test?.outcome) {
    badge = <OutcomeBadge outcome={test.outcome} />
  }
  return <span role="status" className="inline-flex min-h-6 items-center">{badge}</span>
}

// never disabled under the focus: while a test runs or the server's wait lasts it is aria-disabled and ignores presses
export function ProviderTestButton({ name, test, onRun }: { name: string, test?: ProviderTest, onRun: () => void }) {
  const left = useSecondsLeft(test?.until)
  const held = test?.running === true || left > 0
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      aria-label={`Test ${name}`}
      aria-disabled={held || undefined}
      onClick={() => {
        if (!held)
          onRun()
      }}
    >
      {test?.running && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
      Test
    </Button>
  )
}
