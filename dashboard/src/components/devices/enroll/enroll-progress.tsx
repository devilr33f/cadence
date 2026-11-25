import type { CreateOutBody } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useRef } from 'react'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { DeviceStatus } from '@/components/data/device-status'
import { buttonVariants } from '@/components/ui/button-variants'
import { deviceQueryOptions } from '@/lib/devices'
import { EnrollCode } from './enroll-code'
import { enrollHint } from './enroll-command'
import { expiredUnused, isOnline, pollMs } from './enroll-poll'
import { EnrollStep } from './enroll-step'
import { useRemovePending } from './use-remove-pending'

function Waiting({ name, enrolled, expired, error }: { name: string, enrolled: boolean, expired: boolean, error: unknown }) {
  if (error) {
    return (
      <p role="alert" className="text-sm text-destructive">
        {`The speaker could not be read. ${describeError(error)}`}
      </p>
    )
  }
  if (expired)
    return <p className="text-sm text-muted-foreground">{`The code expired before ${name} enrolled.`}</p>
  return (
    <>
      <LoaderCircleIcon aria-hidden="true" className="size-4 text-muted-foreground motion-safe:animate-spin" />
      <p className="text-sm text-muted-foreground">
        {enrolled
          ? `${name} enrolled; waiting for it to connect…`
          : `Waiting for ${name} to enroll with the code…`}
      </p>
    </>
  )
}

// steps 2 and 3: the code and its command until the speaker enrolls with it, then its link
export function EnrollProgress({ created, onRestart, onRemoved }: {
  created: CreateOutBody
  onRestart: () => void
  onRemoved: () => void
}) {
  const api = useApi()
  const removal = useRemovePending(created, onRemoved)
  const expiresAt = Date.parse(created.codeExpiresAt)
  const read = useQuery({
    ...deviceQueryOptions(api, created.id),
    refetchInterval: query => pollMs(query.state, expiresAt),
  })
  const { data, error } = read
  const enrolled = data?.status === 'enrolled'
  const online = isOnline(data)
  // the form that made the code is gone: this step's title takes the focus, and again if the code it shows goes while
  // focused (the speaker used it)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const focusInCodeRef = useRef(false)
  useEffect(() => {
    headingRef.current?.focus()
  }, [])
  useEffect(() => {
    if (enrolled && focusInCodeRef.current) {
      focusInCodeRef.current = false
      headingRef.current?.focus()
    }
  }, [enrolled])

  return (
    <>
      <EnrollStep n={2} title="Run the command" headingRef={headingRef}>
        {enrolled
          ? <p className="text-sm text-muted-foreground">{`${created.name} used its code; it is no longer valid.`}</p>
          : (
              <div
                onFocus={() => {
                  focusInCodeRef.current = true
                }}
                onBlur={() => {
                  focusInCodeRef.current = false
                }}
              >
                <EnrollCode
                  code={created.code}
                  expiresAt={created.codeExpiresAt}
                  hint={enrollHint(created.model, window.location.origin, created.code)}
                  onRestart={onRestart}
                  removal={removal}
                />
              </div>
            )}
      </EnrollStep>
      <EnrollStep n={3} title="Wait for the speaker">
        <div role="status" aria-label="Speaker" className="flex flex-wrap items-center gap-3">
          {online
            ? (
                <>
                  <DeviceStatus status="online" appearance="pill" />
                  <p className="text-sm">{`${created.name} is online.`}</p>
                  <Link to="/devices/$id" params={{ id: created.id }} className={buttonVariants({ size: 'lg' })}>
                    {`Open ${created.name}`}
                  </Link>
                </>
              )
            : <Waiting name={created.name} enrolled={enrolled} expired={expiredUnused(read, expiresAt)} error={error} />}
        </div>
      </EnrollStep>
    </>
  )
}
