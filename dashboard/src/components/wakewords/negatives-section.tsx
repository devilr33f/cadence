import type { WakeWordNegatives } from '@/api/gen'
import { useState } from 'react'
import { describeError } from '@/api/describe-error'
import { KeyValueList } from '@/components/data/key-value-list'
import { StatusBadge } from '@/components/data/status-badge'
import { Button } from '@/components/ui/button'
import { bytesText } from '@/lib/units'

const FETCH_CODES: Readonly<Record<string, string>> = {
  fetch_failed: 'The download failed; huggingface may be unreachable from the server.',
  write_failed: 'The server could not write the file; check its volume.',
}

// the negative features training needs: openWakeWord's, CC BY-NC-SA, fetched into the owner's install on their ask
export function NegativesSection({ negatives, onFetch }: {
  negatives: WakeWordNegatives | undefined
  onFetch: () => Promise<void>
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  if (!negatives)
    return null
  const fetching = negatives.fetching && !negatives.fetching.done
  const failed = negatives.fetching?.done && negatives.fetching.code
  async function fetch() {
    if (pending || fetching)
      return
    setPending(true)
    setError(undefined)
    try {
      await onFetch()
    }
    catch (failure) {
      setError(describeError(failure))
    }
    finally {
      setPending(false)
    }
  }
  return (
    <div className="flex max-w-xl flex-col gap-4 rounded-lg border bg-card p-6">
      {!negatives.configured
        ? (
            <p className="text-sm">
              The server has no negatives file:
              {' '}
              <code className="font-mono text-xs">CADENCE_WAKEWORD_NEGATIVES</code>
              {' '}
              names none, so it cannot train. A ready model can still be uploaded.
            </p>
          )
        : (
            <>
              <div className="flex items-center gap-3">
                {negatives.present
                  ? <StatusBadge tone="success" label="Fetched" />
                  : fetching
                    ? <StatusBadge tone="info" label="Fetching" detail={`${Math.round((negatives.fetching?.fraction ?? 0) * 100)}%`} />
                    : <StatusBadge tone="warning" label="Not fetched" />}
              </div>
              {negatives.present && (
                <KeyValueList items={[
                  { label: 'Windows', value: negatives.windows },
                  { label: 'Streams', value: negatives.streams },
                  {
                    label: 'Size',
                    value: bytesText(negatives.bytes),
                  },
                ]}
                />
              )}
              <p className="max-w-[65ch] text-sm text-muted-foreground">
                Training learns the word against general speech, music and noise: a sample of openWakeWord&apos;s
                precomputed features (about 25 MiB from huggingface, CC BY-NC-SA 4.0). They are fetched into this
                server only, never shipped with Cadence.
              </p>
              <div className="flex flex-col items-start gap-2">
                <Button type="button" variant="outline" aria-disabled={pending || fetching || undefined} onClick={() => void fetch()}>
                  {negatives.present ? 'Fetch them again' : 'Fetch the negatives'}
                </Button>
                {failed && <p role="alert" className="text-sm text-destructive">{FETCH_CODES[negatives.fetching!.code!] ?? negatives.fetching!.code}</p>}
                {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
              </div>
            </>
          )}
    </div>
  )
}
