import type { Entry, Instance } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeftIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { useApi } from '@/api/use-api'
import { LoadingLines } from '@/components/data/states'
import { Tag } from '@/components/data/tag'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { isSubtree, parentPrefix, subtreePrefix, topicsQueryOptions, WILDCARD } from '@/lib/subscriptions'
import { codeText, TOPIC_CODES } from './subscription-refusal'

// an entry's name: the outside system's title as plain text, else its topic
function entryName(entry: Entry): string {
  return entry.title && entry.title.trim() !== ''
    ? entry.title
    : entry.topic
}

function browseError(error: unknown): string {
  return error instanceof ApiError
    ? codeText(TOPIC_CODES, error)
    : describeError(error)
}

function Entries({ prefix, onChoose, onOpen }: { prefix: string, onChoose: (topic: string) => void, onOpen: (prefix: string) => void }) {
  const api = useApi()
  const list = useQuery(topicsQueryOptions(api, prefix))
  if (list.isPending)
    return <LoadingLines lines={2} label="Reading what the module lists" />
  if (list.isError)
    return <p role="alert" className="text-sm text-destructive">{browseError(list.error)}</p>
  if (list.data.entries.length === 0)
    return <p className="text-sm text-muted-foreground">The module lists nothing here.</p>
  return (
    <ul aria-label="What the module lists" className="flex max-h-64 flex-col overflow-y-auto rounded-md border">
      {list.data.entries.map(entry => (
        <li key={entry.topic} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b px-3 py-2 last:border-b-0">
          <div className="flex min-w-0 flex-1 flex-col">
            <span className="text-sm break-words whitespace-pre-wrap">{entryName(entry)}</span>
            <code className="font-mono text-xs break-all text-muted-foreground">{entry.topic}</code>
          </div>
          {entry.kind && <Tag mono>{entry.kind}</Tag>}
          {entry.status && <Tag mono>{entry.status}</Tag>}
          <div className="flex gap-1">
            {isSubtree(entry.topic) && (
              <Button type="button" variant="ghost" size="sm" onClick={() => onOpen(subtreePrefix(entry.topic))}>
                Open
                <span className="sr-only">{`: ${entryName(entry)}`}</span>
              </Button>
            )}
            <Button type="button" variant="outline" size="sm" onClick={() => onChoose(entry.topic)}>
              Choose
              <span className="sr-only">{`: ${entryName(entry)}`}</span>
            </Button>
          </div>
        </li>
      ))}
    </ul>
  )
}

// browses a module's list from one of its providers down (`<module>:<instance>`, then what it lists); a choice fills the
// topic field, which also takes a topic typed by hand
export function TopicPicker({ instances, onChoose }: {
  instances: readonly Instance[]
  onChoose: (topic: string) => void
}) {
  const sourceId = useId()
  const [prefix, setPrefix] = useState<string>()
  const top = prefix?.split(':').slice(0, 2).join(':')
  const up = prefix === undefined
    ? undefined
    : parentPrefix(prefix)
  return (
    <fieldset className="flex flex-col gap-3 rounded-md border p-3">
      <legend className="px-1 text-sm font-medium">Browse</legend>
      <div className="flex flex-col gap-2">
        <Label htmlFor={sourceId}>Provider</Label>
        <NativeSelect
          id={sourceId}
          value={top ?? ''}
          onChange={e => setPrefix(e.target.value === ''
            ? undefined
            : e.target.value)}
        >
          <NativeSelectOption value="">Choose a provider</NativeSelectOption>
          {instances.map(instance => (
            <NativeSelectOption key={instance.id} value={`${instance.module}:${instance.name}`}>
              {`${instance.name} (${instance.module})`}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      {prefix !== undefined && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            {up !== undefined && (
              <Button type="button" variant="ghost" size="sm" onClick={() => setPrefix(up)}>
                <ChevronLeftIcon aria-hidden="true" />
                Up
              </Button>
            )}
            <code className="min-w-0 flex-1 font-mono text-xs break-all">{prefix}</code>
            <Button type="button" variant="outline" size="sm" onClick={() => onChoose(`${prefix}${WILDCARD}`)}>
              Choose everything here
            </Button>
          </div>
          <Entries prefix={prefix} onChoose={onChoose} onOpen={setPrefix} />
        </>
      )}
    </fieldset>
  )
}
