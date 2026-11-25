import type { Topic } from '@/lib/live/topics'
import { PageHeader } from '@/components/data/page-header'
import { useLiveTopics } from '@/lib/live/use-live'
import { RecentActivity } from './recent-activity'
import { SpeakerSummary } from './speaker-summary'

const TOPICS: readonly Topic[] = [
  'devices',
  'sessions',
  'agent',
  'tasks',
]

export function OverviewPage() {
  useLiveTopics(TOPICS)
  return (
    <>
      <PageHeader title="Overview" />
      <div className="flex flex-col gap-8">
        <SpeakerSummary />
        <RecentActivity />
      </div>
    </>
  )
}
