import type { ReactNode } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'

// the server's message on a field it refused, in codes
export function refusedText(error: ApiError): string {
  return `The server refused this value (${error.error}).`
}

// the server is the authority on validity: its 422 path lands on the field it names (refuse says whether one has it),
// else the answer goes beside the form with the path itself; anything else is worded in codes. undefined: on a field
export function refusalOf(error: unknown, refuse: (pointer: string, message: string) => boolean): ReactNode | undefined {
  if (!(error instanceof ApiError) || error.path === undefined)
    return describeError(error)
  if (refuse(error.path, refusedText(error)))
    return undefined
  return (
    <>
      {`The server answered ${error.status} ${error.error} at `}
      <code className="font-mono text-xs">
        {error.path === ''
          ? '/'
          : error.path}
      </code>
      .
    </>
  )
}
