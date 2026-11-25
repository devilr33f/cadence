import type { EnrollHint } from './enroll-command'
import type { Removal } from './use-remove-pending'
import { Link } from '@tanstack/react-router'
import { LoaderCircleIcon, TimerOffIcon } from 'lucide-react'
import { useEffect, useId, useRef } from 'react'
import { CopyButton } from '@/components/data/copy-button'
import { Button } from '@/components/ui/button'
import { useNow } from '@/hooks/use-now'
import { durationText } from '@/lib/format-time'

// the code is shown here and nowhere else: never logged, stored or cached, and gone with this page
export function EnrollCode({ code, expiresAt, hint, onRestart, removal }: {
  code: string
  expiresAt: string
  hint: EnrollHint
  onRestart: () => void
  // the code's pending device, which an expired code leaves behind
  removal?: Removal
}) {
  const now = useNow()
  const captionId = useId()
  const left = now === null
    ? null
    : Date.parse(expiresAt) - now
  const expired = left !== null && left <= 0

  // the copy button goes when the code expires: if it held the focus, the way to a new code takes it
  const focusInCodeRef = useRef(false)
  const restartRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (expired && focusInCodeRef.current) {
      focusInCodeRef.current = false
      restartRef.current?.focus()
    }
  }, [expired])

  if (expired) {
    return (
      <div role="alert" className="flex flex-col items-start gap-3 rounded-md border bg-warning-surface p-4">
        <p className="flex items-center gap-2 font-medium text-warning">
          <TimerOffIcon aria-hidden="true" className="size-4" />
          The code expired.
        </p>
        <p className="text-sm">
          A speaker that has not enrolled with it needs a new one.
          {removal && ` ${removal.name} stays in the list, its name taken, until it is removed.`}
        </p>
        <div className="flex flex-wrap gap-2">
          <Button ref={restartRef} type="button" variant="outline" onClick={onRestart}>Make a new code</Button>
          {removal && (
            // focusable while it works, so a refusal finds the focus where it was
            <Button type="button" variant="ghost" aria-disabled={removal.pending || undefined} onClick={() => void removal.remove()}>
              {removal.pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
              {`Remove ${removal.name}`}
            </Button>
          )}
        </div>
        {removal?.error && <p className="text-sm text-destructive">{removal.error}</p>}
      </div>
    )
  }

  return (
    <div
      className="flex flex-col gap-4"
      onFocus={() => {
        focusInCodeRef.current = true
      }}
      onBlur={() => {
        focusInCodeRef.current = false
      }}
    >
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <p className="font-mono text-2xl font-semibold tracking-wider">{code}</p>
        <p className="text-sm text-muted-foreground tabular-nums">
          {left === null
            ? 'Expires soon'
            : `${durationText(left)} left`}
        </p>
      </div>
      {hint.screen && (
        <div className="flex flex-col gap-1 rounded-md border bg-muted/40 p-3 text-sm">
          <p className="font-medium">A screen joins by itself</p>
          <p>
            Started without a code, it shows one of its own and a QR: approve it under
            {' '}
            <Link to="/claims" className="underline underline-offset-4">Screens waiting</Link>
            {' '}
            with a name and a room. This code is the fallback: type it on the TV under «У меня есть код из панели».
          </p>
        </div>
      )}
      {hint.command && (
        <>
          <figure aria-labelledby={captionId} className="flex flex-col gap-2">
            <figcaption id={captionId} className="text-sm font-medium">Command to run</figcaption>
            <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-xs/5 whitespace-pre-wrap break-all"><code>{hint.command}</code></pre>
            {hint.readme && (
              <p className="text-sm text-muted-foreground">
                From the repo root, with
                {' '}
                <code className="font-mono text-xs">$speaker</code>
                {' '}
                and
                {' '}
                <code className="font-mono text-xs">$tarball</code>
                {' '}
                as
                {' '}
                <code className="font-mono text-xs">{hint.readme}</code>
                {' '}
                sets them.
              </p>
            )}
          </figure>
          <CopyButton text={hint.command} label="Copy the command" />
        </>
      )}
    </div>
  )
}
