import type { ReactNode, RefObject } from 'react'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { PlusIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { PageHeader } from '@/components/data/page-header'
import { Button } from '@/components/ui/button'
import { useDeviceNames } from '@/hooks/use-device-names'
import { devicesQueryOptions } from '@/lib/devices'
import { useLiveTopics } from '@/lib/live/use-live'
import { useMemoryActions } from '@/lib/memory'
import { LearningLog } from './learning-log'
import { createBody, valuesOf } from './memory-body'
import { MemoryDialog } from './memory-dialog'
import { MemoryList } from './memory-list'
import { SuggestionInbox } from './suggestion-inbox'

// memory_changed, learning_run, learning_op and compaction refresh what they changed
const TOPICS: readonly Topic[] = ['agent']

function Section({ title, description, headingRef, children }: {
  title: string
  description: string
  headingRef?: RefObject<HTMLHeadingElement | null>
  children: ReactNode
}) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <div>
        <h2
          id={titleId}
          ref={headingRef}
          tabIndex={headingRef
            ? -1
            : undefined}
          className="text-lg font-semibold outline-none"
        >
          {title}
        </h2>
        <p className="max-w-[65ch] text-base text-muted-foreground">{description}</p>
      </div>
      {children}
    </section>
  )
}

export function MemoryPage() {
  const api = useApi()
  useLiveTopics(TOPICS)
  const devices = useQuery(devicesQueryOptions(api)).data ?? []
  const names = useDeviceNames()
  const actions = useMemoryActions()
  const [creating, setCreating] = useState(false)
  // the page's action takes the focus back from a dialog whose row went (a delete), and from the create
  const addRef = useRef<HTMLButtonElement>(null)
  // an approval or a rejection takes its row away: the focus waits on the suggestions' heading
  const suggestionsRef = useRef<HTMLHeadingElement>(null)

  const add = (size: 'default' | 'lg') => (
    <Button
      ref={size === 'lg'
        ? addRef
        : undefined}
      size={size}
      onClick={() => setCreating(true)}
    >
      <PlusIcon aria-hidden="true" />
      Add a memory
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Memory"
        description="What the assistant remembers about the household and each speaker, what learning proposes, and what each conversation taught it."
        actions={add('lg')}
      />
      <div className="flex flex-col gap-10">
        <Section
          title="Suggestions"
          description="With memory approval on, learning changes nothing by itself: approve, edit or reject each suggestion."
          headingRef={suggestionsRef}
        >
          <SuggestionInbox devices={devices} names={names} heading={suggestionsRef} />
        </Section>
        <Section title="Memories" description="Active memories, the household's and each speaker's. A pinned one reaches every turn of its scope.">
          <MemoryList devices={devices} names={names} add={add('default')} finalFocus={addRef} />
        </Section>
        <Section title="Learning" description="A speaker's learning runs and conversation compactions, newest first.">
          <LearningLog devices={devices} />
        </Section>
      </div>
      <MemoryDialog
        open={creating}
        onOpenChange={setCreating}
        title="Add a memory"
        description="Something the assistant should know, for the whole household or for one speaker."
        finalFocus={addRef}
        initial={valuesOf()}
        devices={devices}
        submitLabel="Add the memory"
        onSubmit={values => actions.create(createBody(values))}
      />
    </>
  )
}
