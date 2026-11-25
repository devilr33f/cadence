import type { ReactNode, RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AudioLinesIcon } from 'lucide-react'
import { useRef } from 'react'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { buttonVariants } from '@/components/ui/button-variants'
import { useLiveTopics } from '@/lib/live/use-live'
import { useTimeZone } from '@/lib/time-zone'
import { negativesQueryOptions, useWakeWordActions, wakeWordsQueryOptions } from '@/lib/wakewords'
import { NegativesSection } from './negatives-section'
import { TrainForm } from './train-form'
import { UploadModelForm } from './upload-model-form'
import { wakeWordColumns } from './wake-word-table'

const TOPICS = ['wakewords'] as const
const NEWEST = { column: 'created', direction: 'desc' } as const

function Section({ id, title, headingRef, children }: {
  id: string
  title: string
  headingRef?: RefObject<HTMLHeadingElement | null>
  children: ReactNode
}) {
  return (
    <section aria-labelledby={id} className="mt-8 flex flex-col gap-4">
      <h2 id={id} ref={headingRef} tabIndex={-1} className="border-b pb-2 text-lg font-semibold">{title}</h2>
      {children}
    </section>
  )
}

export function WakeWordsPage() {
  useLiveTopics(TOPICS)
  const api = useApi()
  const timeZone = useTimeZone()
  const list = useQuery(wakeWordsQueryOptions(api))
  const negatives = useQuery(negativesQueryOptions(api))
  const actions = useWakeWordActions()
  const wordsHeadingRef = useRef<HTMLHeadingElement>(null)

  return (
    <>
      <PageHeader
        title="Wake words"
        description="A word of your own that wakes a speaker, beside or instead of its built-in one. Train it here from a few takes, then switch it on per device on the device's page."
      />
      <Section id="wake-words" title="Words" headingRef={wordsHeadingRef}>
        <DataTable
          caption="Wake words"
          columns={wakeWordColumns(timeZone)}
          rows={list.data}
          rowKey={w => w.id}
          initialSort={NEWEST}
          status={list.isError
            ? 'error'
            : list.isPending ? 'loading' : 'ready'}
          error={<ErrorState title="The wake words could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
          empty={(
            <EmptyState
              icon={AudioLinesIcon}
              title="No wake words"
              description="Train one from your takes below, or upload a model."
            />
          )}
          rowActions={w => (
            <div className="flex justify-end gap-1">
              {w.status === 'ready' && (
                <a
                  href={`/api/v1/wakewords/${w.id}/model`}
                  download={`${w.word}.json`}
                  className={buttonVariants({ variant: 'ghost', size: 'sm' })}
                  aria-label={`Download the model: ${w.word}`}
                >
                  Download
                </a>
              )}
              {w.datasetKept && w.status !== 'training' && (
                <ConfirmDialog
                  trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Retrain: ${w.word}`}>Retrain</Button>}
                  title={`Train ${w.word} again?`}
                  description="It trains from its kept recordings; until it is ready, devices keep the model they have."
                  confirmLabel="Train again"
                  inlineError
                  onConfirm={() => actions.retrain(w.id)}
                />
              )}
              {w.datasetKept && w.status !== 'training' && (
                <ConfirmDialog
                  trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Delete the recordings: ${w.word}`}>Delete recordings</Button>}
                  title={`Delete the recordings of ${w.word}?`}
                  description="The model stays; it can no longer be trained again without new takes."
                  confirmLabel="Delete the recordings"
                  tone="destructive"
                  inlineError
                  onConfirm={() => actions.dropDataset(w.id)}
                />
              )}
              <ConfirmDialog
                trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Delete: ${w.word}`}>Delete</Button>}
                title={`Delete ${w.word}?`}
                description="Devices that run it keep their copy until their configuration changes; nothing new can switch it on."
                confirmLabel="Delete the word"
                tone="destructive"
                inlineError
                finalFocus={wordsHeadingRef}
                onConfirm={() => actions.remove(w.id)}
              />
            </div>
          )}
        />
      </Section>
      <Section id="wake-train" title="Train a word">
        <TrainForm onTrain={actions.train} />
      </Section>
      <Section id="wake-negatives" title="Negative features">
        <NegativesSection negatives={negatives.data} onFetch={actions.fetchNegatives} />
      </Section>
      <Section id="wake-upload" title="Upload a model">
        <UploadModelForm onUpload={actions.uploadModel} />
      </Section>
    </>
  )
}
