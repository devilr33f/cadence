import type { ReactNode } from 'react'
import type { Deleted } from './use-leave-deleted'
import type { Detail } from '@/api/gen'
import type { Topic } from '@/lib/live/topics'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { ChevronLeftIcon, PlugIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { applyState } from '@/components/data/apply-state'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { RecentlyPlayed } from '@/components/history/recently-played'
import { SettingsEditor } from '@/components/settings/settings-editor'
import { Tabs, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { cachedDeviceName, deviceQueryOptions, useDeviceActions } from '@/lib/devices'
import { useDeviceDeleted, useLiveTopics } from '@/lib/live/use-live'
import { isScreen } from '../tv/tv'
import { TvSection } from '../tv/tv-section'
import { AlarmsCard } from './alarms/alarms-card'
import { AlarmsTab } from './alarms/alarms-tab'
import { AnnounceBox } from './announce-box'
import { AssistantChat } from './assistant/assistant-chat'
import { CapabilityCard } from './capability-card'
import { CecCard } from './cec-card'
import { Conversations } from './conversations/conversations'
import { DangerZone } from './danger-zone'
import { DeviceHeader } from './device-header'
import { DeviceTimeline } from './device-timeline'
import { Health } from './health/health'
import { Inbox } from './inbox/inbox'
import { MediaCard } from './media-card'
import { PlayUrlBox } from './play-url-box'
import { UpdatesTab } from './updates/updates-tab'
import { useLeaveDeleted } from './use-leave-deleted'
import { WakeWordCard } from './wake-word-card'
import { WiFiCard } from './wifi-card'

// devices: its own presence, rename, volume and delete; alarms: its alarms' state and rings; ops: its update runs
const TOPICS: readonly Topic[] = [
  'devices',
  'sessions',
  'agent',
  'media',
  'alarms',
  'ops',
]

// a change on its way to a device that is online is followed closely until it acknowledges it; an offline one cannot
// take it before it reconnects
const PENDING_REFRESH_MS = 2000
// a capability's state reaches no event (its volume aside), so the detail is also read on this beat
const DETAIL_REFRESH_MS = 30_000

function refreshMs(detail?: Detail): number {
  return detail?.online && detail.capabilities?.some(c => applyState(c) === 'pending')
    ? PENDING_REFRESH_MS
    : DETAIL_REFRESH_MS
}

function Section({ title, children }: { title: string, children: ReactNode }) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <h2 id={titleId} className="text-lg font-semibold">{title}</h2>
      {children}
    </section>
  )
}

function BackLink() {
  return (
    <Link to="/devices" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ChevronLeftIcon aria-hidden="true" className="size-4" />
      Devices
    </Link>
  )
}

function Capabilities({ device, onOpenAlarms }: { device: Detail, onOpenAlarms: () => void }) {
  const capabilities = device.capabilities ?? []
  if (capabilities.length === 0) {
    return (
      <div className="rounded-lg border bg-card">
        <EmptyState icon={PlugIcon} title="No capabilities yet" description="The speaker reports what it can do once it connects." />
      </div>
    )
  }
  // alarms has its own summary and tab: the generic one would offer set and cancel, which need a woken session (rule 7);
  // display.cec's outputs get their own buttons, network.wifi its scan and connect (the name and password as one
  // generation), and wake.custom a switch and a picker of the server's words
  return capabilities.map(c => c.id === 'alarms'
    ? <AlarmsCard key={c.id} deviceId={device.id} capability={c} onOpen={onOpenAlarms} />
    : c.id === 'display.cec'
      ? <CecCard key={c.id} deviceId={device.id} capability={c} />
      : c.id === 'network.wifi'
        ? <WiFiCard key={c.id} deviceId={device.id} capability={c} />
        : c.id === 'wake.custom'
          ? <WakeWordCard key={c.id} deviceId={device.id} capability={c} />
          : <CapabilityCard key={c.id} deviceId={device.id} capability={c} />)
}

export function DevicePage() {
  const { id } = useParams({ from: '/_authed/devices/$id' })
  useLiveTopics(TOPICS)
  const api = useApi()
  const queryClient = useQueryClient()
  const actions = useDeviceActions(id)
  const [deleted, setDeleted] = useState<Deleted>()
  // set while this page's own delete is on its way: the server's word of it may come before the answer
  const deletingRef = useRef(false)
  // the first word of a delete leaves, whichever comes first: the delete's answer or the feed's
  const leave = (next: Deleted) => setDeleted(current => current?.id === next.id
    ? current
    : next)
  useDeviceDeleted(id, () => leave({
    id,
    name: cachedDeviceName(queryClient, api, id) ?? 'The speaker',
    elsewhere: !deletingRef.current,
  }))
  useLeaveDeleted(deleted)
  // a deleted device leaves the page at once, every query that reads it with it, before the page goes to the list
  if (deleted?.id === id)
    return <BackLink />

  async function remove(name: string) {
    deletingRef.current = true
    try {
      await actions.remove()
    }
    finally {
      deletingRef.current = false
    }
    leave({ id, name })
  }

  // one route serves every device: nothing open on one (an editor and its draft) may carry over to the next
  return <DeviceView key={id} id={id} onDelete={remove} />
}

function DeviceView({ id, onDelete }: { id: string, onDelete: (name: string) => Promise<void> }) {
  const api = useApi()
  const [selectedTab, setSelectedTab] = useState('overview')
  const alarmsTabRef = useRef<HTMLButtonElement>(null)
  const detail = useQuery({
    ...deviceQueryOptions(api, id),
    refetchInterval: query => refreshMs(query.state.data),
  })

  if (detail.isPending) {
    return (
      <>
        <BackLink />
        <LoadingLines lines={4} label="Loading the device" />
      </>
    )
  }
  if (detail.isError) {
    return (
      <>
        <BackLink />
        <div className="rounded-lg border bg-card">
          <ErrorState title="This device could not be shown" error={detail.error} onRetry={() => void detail.refetch()} />
        </div>
      </>
    )
  }

  const device = detail.data
  const hasAlarms = device.capabilities?.some(c => c.id === 'alarms') ?? false
  const activeTab = selectedTab === 'alarms' && !hasAlarms
    ? 'overview'
    : selectedTab
  function openAlarms() {
    setSelectedTab('alarms')
    requestAnimationFrame(() => alarmsTabRef.current?.focus())
  }
  return (
    <>
      <BackLink />
      <DeviceHeader device={device} />
      {/* overview stays mounted while hidden, so an open editor keeps its draft; alarms, health, the conversations and
          the inbox mount, and are read, only while their tab shows */}
      <Tabs value={activeTab} onValueChange={setSelectedTab}>
        <TabsList>
          <TabsTab value="overview">Overview</TabsTab>
          <TabsTab value="assistant">Assistant</TabsTab>
          {hasAlarms && <TabsTab ref={alarmsTabRef} value="alarms">Alarms</TabsTab>}
          <TabsTab value="updates">Updates</TabsTab>
          <TabsTab value="health">Health</TabsTab>
          <TabsTab value="conversations">Conversations</TabsTab>
          <TabsTab value="inbox">Inbox</TabsTab>
        </TabsList>
        <TabsPanel value="overview" keepMounted className="flex flex-col gap-8">
          <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_20rem]">
            <div className="flex min-w-0 flex-col gap-8">
              {isScreen(device) && (
                <Section title="TV">
                  <TvSection device={device} />
                </Section>
              )}
              <Section title="Capabilities">
                <Capabilities device={device} onOpenAlarms={openAlarms} />
              </Section>
              <Section title="Settings">
                <SettingsEditor scope={{ kind: 'device', deviceId: device.id }} />
              </Section>
              <Section title="Activity">
                <DeviceTimeline deviceId={device.id} deviceName={device.name} />
              </Section>
            </div>
            {/* on a phone what plays comes before the long list of capabilities */}
            <div className="flex flex-col gap-6 max-lg:order-first">
              <MediaCard deviceId={device.id} output={device.capabilities?.find(c => c.id === 'audio.output')} />
              <RecentlyPlayed deviceId={device.id} />
              <AnnounceBox deviceId={device.id} deviceName={device.name} />
              {/* a screen's audiod has no url source: older tv apps still list media.url and refuse every play */}
              {!isScreen(device) && device.capabilities?.some(c => c.id === 'media.url' && c.commands.some(cmd => cmd.name === 'play'))
                && <PlayUrlBox deviceId={device.id} deviceName={device.name} />}
            </div>
          </div>
          <DangerZone device={device} onDelete={() => onDelete(device.name)} />
        </TabsPanel>
        {/* kept mounted, so the requests asked stay while another tab shows */}
        <TabsPanel value="assistant" keepMounted>
          <AssistantChat deviceId={device.id} deviceName={device.name} />
        </TabsPanel>
        {hasAlarms && (
          <TabsPanel value="alarms">
            <AlarmsTab device={device} />
          </TabsPanel>
        )}
        <TabsPanel value="updates">
          <UpdatesTab device={device} />
        </TabsPanel>
        <TabsPanel value="health">
          <Health device={device} />
        </TabsPanel>
        <TabsPanel value="conversations">
          <Conversations deviceId={device.id} deviceName={device.name} />
        </TabsPanel>
        <TabsPanel value="inbox">
          <Inbox deviceId={device.id} />
        </TabsPanel>
      </Tabs>
    </>
  )
}
