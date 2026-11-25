import { SpeakerIcon } from 'lucide-react'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { useSpeakerCounts } from './use-speaker-counts'

// how many speakers are up, in words, glyphs and colour, as one line rather than headline numbers
export function SpeakerSummary() {
  const speakers = useSpeakerCounts()
  return (
    <section aria-labelledby="speakers-title">
      <h2 id="speakers-title" className="mb-3 text-lg font-semibold">Speakers</h2>
      <div className="rounded-lg border bg-card">
        {speakers.status === 'loading' && <LoadingLines lines={1} label="Loading speakers" className="p-4" />}
        {speakers.status === 'error' && (
          <ErrorState title="The speakers could not be counted" error={speakers.error} onRetry={speakers.retry} />
        )}
        {speakers.status === 'ready' && speakers.total === 0 && (
          <EmptyState
            icon={SpeakerIcon}
            title="No speakers yet"
            description="Each speaker you enroll shows here, with whether it is online."
          />
        )}
        {speakers.status === 'ready' && speakers.total > 0 && (
          <ul aria-label="Speakers by state" className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
            <li><StatusBadge tone="success" label={`${speakers.online} online`} /></li>
            <li><StatusBadge tone="neutral" label={`${speakers.offline} offline`} /></li>
            {speakers.enrolling > 0 && (
              <li><StatusBadge tone="neutral" glyph="dashed" label={`${speakers.enrolling} enrolling`} /></li>
            )}
          </ul>
        )}
      </div>
    </section>
  )
}
