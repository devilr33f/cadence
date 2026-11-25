import type { ReactNode } from 'react'
import type { KeyValue } from '@/components/data/key-value-list'
import { useQuery } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { useApi } from '@/api/use-api'
import { KeyValueList } from '@/components/data/key-value-list'
import { LoadingLines } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { inspectQueryOptions, transcriptQueryOptions } from '@/lib/tasks'

function Text({ children }: { children: ReactNode }) {
  return <span className="break-words whitespace-pre-wrap">{children}</span>
}

// the run's own words, as the kind gives them: text in a block, never markup
function Transcript({ taskId }: { taskId: string }) {
  const api = useApi()
  const captionId = useId()
  const transcript = useQuery({
    ...transcriptQueryOptions(api, taskId),
    retry: false,
  })
  if (transcript.isPending)
    return <LoadingLines lines={4} label="Loading the transcript" />
  if (transcript.isError)
    return <p role="alert" className="text-sm text-destructive">{`The transcript could not be read. ${describeError(transcript.error)}`}</p>
  return (
    <figure aria-labelledby={captionId} className="flex flex-col gap-2">
      <figcaption id={captionId} className="text-sm font-medium">Transcript</figcaption>
      <pre className="max-h-[60svh] overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs/5 break-words whitespace-pre-wrap">{transcript.data.transcript ?? ''}</pre>
    </figure>
  )
}

// the kind's live view of the run (its progress, result and code), the transcript on request; a kind that is off or
// silent answers 502, which leaves the rest of the page as it is
export function TaskLiveView({ taskId }: { taskId: string }) {
  const api = useApi()
  const [showTranscript, setShowTranscript] = useState(false)
  const transcriptId = useId()
  const view = useQuery({
    ...inspectQueryOptions(api, taskId),
    retry: false,
  })
  if (view.isPending)
    return <LoadingLines lines={3} label="Loading the live view" />
  if (view.isError)
    return <p className="text-sm text-muted-foreground">{`The live view is unavailable. ${describeError(view.error)}`}</p>

  // the status is the page's pill: the view adds what the kind reports beside it
  const { progress, result, errorCode } = view.data
  const items: KeyValue[] = [
    {
      label: 'Progress',
      value: progress
        ? <Text>{progress}</Text>
        : <span className="text-muted-foreground">None reported</span>,
    },
    ...(result
      ? [
          { label: 'Result', value: <Text>{result}</Text> },
        ]
      : []),
    ...(errorCode
      ? [
          { label: 'Error', value: errorCode, mono: true },
        ]
      : []),
  ]
  return (
    <div className="flex flex-col gap-4">
      <KeyValueList items={items} />
      {/* it opens in place below its toggle, which keeps the focus and closes it again */}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="self-start"
        aria-expanded={showTranscript}
        aria-controls={transcriptId}
        onClick={() => setShowTranscript(shown => !shown)}
      >
        {showTranscript ? 'Hide the transcript' : 'Show the transcript'}
      </Button>
      <div id={transcriptId} className="empty:hidden">
        {showTranscript && <Transcript taskId={taskId} />}
      </div>
    </div>
  )
}
