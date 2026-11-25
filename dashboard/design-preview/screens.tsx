import type { SampleCapability, SampleDevice } from './sample-data'
// the preview's pages, composed from the real components with synthetic data (sample-data.ts)
import type { Column } from '@/components/data/data-table'
import { ChevronLeftIcon, EllipsisIcon, PauseIcon, PencilIcon, PlusIcon, SkipBackIcon, SkipForwardIcon, SpeakerIcon } from 'lucide-react'
import { ApiError } from '@/api/error'
import { ApplyBadge } from '@/components/data/apply-badge'
import { DataTable } from '@/components/data/data-table'
import { DeviceStatus } from '@/components/data/device-status'
import { KeyValueList } from '@/components/data/key-value-list'
import { LogView } from '@/components/data/log-view'
import { PageHeader } from '@/components/data/page-header'
import { SeverityLabel } from '@/components/data/severity-label'
import { EmptyState, ErrorState } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { Timeline } from '@/components/data/timeline'
import { FeedStatus } from '@/components/shell/feed-status'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatClock } from '@/lib/format-time'
import { CAPABILITIES, DEVICES, LEDS_DIFF, LOG, TIME_ZONE } from './sample-data'

const deviceColumns: Column<SampleDevice>[] = [
  {
    id: 'name',
    header: 'Name',
    sortValue: d => d.name,
    cell: d => (
      <span className="flex items-baseline gap-2">
        <a href="#detail" className="font-medium underline-offset-4 hover:underline">{d.name}</a>
        <span className="font-mono text-xs text-muted-foreground">{d.id.slice(-6)}</span>
      </span>
    ),
  },
  {
    id: 'status',
    header: 'Status',
    sortValue: d => d.status,
    cell: d => <DeviceStatus status={d.status} />,
  },
  {
    id: 'config',
    header: 'Configuration',
    cell: d => <ApplyBadge appearance="inline" {...d} />,
  },
  {
    id: 'model',
    header: 'Board',
    sortValue: d => d.model,
    cell: d => <span className="font-mono text-xs">{d.model}</span>,
    className: 'max-lg:hidden',
  },
  {
    id: 'seen',
    header: 'Last seen',
    sortValue: d => d.lastSeenAt,
    firstSort: 'desc',
    cell: d => (
      <span className="flex items-baseline gap-2">
        <time dateTime={d.lastSeenAt} className="font-semibold">{formatClock(d.lastSeenAt, TIME_ZONE)}</time>
        <span className="text-muted-foreground">{d.lastSeen}</span>
      </span>
    ),
  },
]

export function DevicesScreen() {
  return (
    <>
      <PageHeader
        title="Devices"
        description="Every speaker in the household: its link, its configuration and when it was last heard from."
        actions={(
          <Button size="lg">
            <PlusIcon aria-hidden="true" />
            Enroll a speaker
          </Button>
        )}
      />
      <DataTable
        caption="Devices"
        columns={deviceColumns}
        rows={DEVICES}
        rowKey={d => d.id}
        empty={null}
        rowActions={d => (
          <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${d.name}`}>
            <EllipsisIcon aria-hidden="true" />
          </Button>
        )}
      />
    </>
  )
}

const capabilityColumns: Column<SampleCapability>[] = [
  {
    id: 'id',
    header: 'Capability',
    sortValue: c => c.id,
    cell: c => <span className="font-mono text-xs">{c.id}</span>,
  },
  {
    id: 'version',
    header: 'Version',
    cell: c => <span className="text-muted-foreground">{`v${c.version}`}</span>,
    className: 'max-sm:hidden',
  },
  {
    id: 'config',
    header: 'Configuration',
    cell: c => <ApplyBadge appearance="inline" {...c} />,
  },
  {
    id: 'state',
    header: 'State',
    cell: c => <span className="text-muted-foreground">{c.state}</span>,
    className: 'max-md:hidden',
  },
]

function Section({ title, children, action }: { title: string, children: React.ReactNode, action?: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-4">
        <h2 className="text-lg font-semibold">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  )
}

function DesiredVsApplied() {
  const leds = CAPABILITIES[1]!
  return (
    <Card size="sm">
      <CardHeader className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <CardTitle className="font-mono text-sm font-medium">leds</CardTitle>
        <ApplyBadge {...leds} />
        <p className="basis-full text-sm text-muted-foreground">
          Sent at
          {' '}
          <time dateTime="2026-11-12T15:03:58Z" className="font-semibold text-foreground">21:03:58</time>
          . The speaker applies it once the reply it is playing ends.
        </p>
      </CardHeader>
      <CardContent>
        <table className="w-full border-separate border-spacing-0 text-sm">
          <caption className="sr-only">leds: desired and applied settings</caption>
          <thead>
            <tr className="text-left text-xs text-muted-foreground">
              <th className="border-b py-1.5 pr-3 font-medium">Setting</th>
              <th className="border-b px-3 py-1.5 font-medium">Desired</th>
              <th className="border-b py-1.5 pl-3 font-medium">Applied</th>
            </tr>
          </thead>
          <tbody>
            {LEDS_DIFF.map(row => (
              <tr
                key={row.setting}
                className={row.desired === row.applied
                  ? undefined
                  : 'bg-accent'}
              >
                <td className="h-(--row-height) border-b py-1 pr-3 pl-2 font-mono text-xs">{row.setting}</td>
                <td className="border-b px-3 py-1 font-medium">{row.desired}</td>
                <td className="border-b py-1 pl-3">
                  {row.applied}
                  {row.desired !== row.applied && <span className="sr-only"> (not applied yet)</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </CardContent>
    </Card>
  )
}

// an authored cover for the sample track: a record, in shapes the page can state exactly
function SampleCover() {
  return (
    <svg viewBox="0 0 64 64" role="img" aria-label="Cover art (sample)" className="size-16 shrink-0 rounded-md">
      <rect width="64" height="64" fill="oklch(0.36 0.07 230)" />
      <circle cx="40" cy="32" r="22" fill="oklch(0.2 0.03 250)" />
      <circle cx="40" cy="32" r="16" fill="none" stroke="oklch(0.3 0.03 250)" strokeWidth="1" />
      <circle cx="40" cy="32" r="11" fill="none" stroke="oklch(0.3 0.03 250)" strokeWidth="1" />
      <circle cx="40" cy="32" r="6" fill="oklch(0.87 0.165 92)" />
      <rect x="8" y="10" width="14" height="44" rx="2" fill="oklch(0.93 0.02 90)" />
    </svg>
  )
}

function NowPlaying() {
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>Media</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex gap-3">
          <SampleCover />
          <div className="min-w-0">
            <p className="truncate font-medium">Clair de lune</p>
            <p className="truncate text-sm text-muted-foreground">Claude Debussy · Suite bergamasque</p>
            <StatusBadge tone="success" label="Playing" detail="Spotify" className="mt-1" />
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <div className="h-1 overflow-hidden rounded-full bg-muted" role="progressbar" aria-label="Position" aria-valuemin={0} aria-valuemax={302} aria-valuenow={161}>
            <div className="h-full w-[53%] rounded-full bg-foreground" />
          </div>
          <div className="flex justify-between text-xs text-muted-foreground tabular-nums">
            <span>2:41</span>
            <span>5:02</span>
          </div>
        </div>
        <div className="flex items-center justify-center gap-2">
          <Button variant="ghost" size="icon" aria-label="Previous"><SkipBackIcon aria-hidden="true" /></Button>
          <Button variant="outline" size="icon-lg" aria-label="Pause"><PauseIcon aria-hidden="true" /></Button>
          <Button variant="ghost" size="icon" aria-label="Next"><SkipForwardIcon aria-hidden="true" /></Button>
        </div>
      </CardContent>
    </Card>
  )
}

export function DeviceDetailScreen() {
  const kitchen = DEVICES[0]!
  return (
    <>
      <a href="#devices" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ChevronLeftIcon aria-hidden="true" className="size-4" />
        Devices
      </a>
      <div className="mb-6 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 className="text-xl font-semibold">{kitchen.name}</h1>
        <DeviceStatus status={kitchen.status} appearance="pill" />
        <span className="font-mono text-xs text-muted-foreground">{kitchen.model}</span>
      </div>

      <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="flex min-w-0 flex-col gap-8">
          <Section title="Configuration">
            <DataTable
              caption="Capabilities"
              columns={capabilityColumns}
              rows={CAPABILITIES}
              rowKey={c => c.id}
              empty={null}
              rowActions={c => (
                <Button variant="ghost" size="sm" aria-label={`Edit ${c.id}`}>
                  <PencilIcon aria-hidden="true" />
                  Edit
                </Button>
              )}
            />
            <DesiredVsApplied />
          </Section>

          <Section title="Activity">
            <Timeline
              label="Kitchen activity"
              live
              timeZone={TIME_ZONE}
              entries={[
                {
                  id: 't1',
                  at: '2026-11-12T15:04:12Z',
                  kind: 'turn',
                  text: <>Woken by voice: «какая погода завтра»</>,
                  detail: (
                    <KeyValueList
                      items={[
                        { label: 'Session', value: '0193a71e-55c0-7e2b-b1a4-6f0d93c2e7aa', mono: true },
                        { label: 'Mode', value: 'fast' },
                        { label: 'Speech end → first audio', value: '1 180 ms' },
                      ]}
                    />
                  ),
                },
                {
                  id: 't2',
                  at: '2026-11-12T15:04:13Z',
                  kind: 'tool',
                  text: <>Weather for Bishkek, tomorrow</>,
                },
                {
                  id: 't3',
                  at: '2026-11-12T15:04:14Z',
                  kind: 'reply',
                  text: <>Replied in 6.2 s</>,
                },
                {
                  id: 't4',
                  at: '2026-11-12T15:03:58Z',
                  kind: 'config',
                  text: <>leds gen 4 sent</>,
                },
                {
                  id: 't5',
                  at: '2026-11-12T14:58:03Z',
                  kind: 'media',
                  text: <>Spotify started playing</>,
                },
                {
                  id: 't6',
                  at: '2026-11-12T14:41:37Z',
                  kind: 'error',
                  text: <>alarms gen 7 failed: time_zone_unset</>,
                },
              ]}
            />
          </Section>
        </div>

        <div className="flex flex-col gap-6">
          <NowPlaying />
          <Card size="sm">
            <CardHeader>
              <CardTitle>State</CardTitle>
            </CardHeader>
            <CardContent>
              <KeyValueList
                items={[
                  { label: 'Volume', value: '35 %' },
                  { label: 'Microphone', value: 'on' },
                  { label: 'Follow-up', value: 'on, 5 s' },
                  { label: 'Last seen', value: <time dateTime={kitchen.lastSeenAt} className="font-semibold">21:04:12</time> },
                  { label: 'Daemon', value: '2.0.0-m7', mono: true },
                  { label: 'Device id', value: kitchen.id, mono: true },
                ]}
              />
            </CardContent>
          </Card>
        </div>
      </div>

      <div className="mt-8">
        <Section title="Log">
          <LogView label="Kitchen log" records={LOG} timeZone={TIME_ZONE} className="max-h-72" />
        </Section>
      </div>
    </>
  )
}

const emptyColumns: Column<SampleDevice>[] = deviceColumns.slice(0, 3)

export function StatesScreen() {
  return (
    <>
      <PageHeader title="States" description="The vocabulary every page uses: each state reads in colour, in shape and in words." />
      <div className="flex flex-col gap-10">
        <Section title="Status">
          <div className="grid gap-x-10 gap-y-5 rounded-lg border bg-card p-5 sm:grid-cols-2 xl:grid-cols-4">
            <div className="flex flex-col gap-2">
              <h3 className="text-sm text-muted-foreground">Device</h3>
              <DeviceStatus status="online" />
              <DeviceStatus status="updating" />
              <DeviceStatus status="offline" />
              <DeviceStatus status="enrolling" />
            </div>
            <div className="flex flex-col items-start gap-2">
              <h3 className="text-sm text-muted-foreground">Desired vs applied</h3>
              <ApplyBadge desiredGen={14} appliedGen={14} appliedError="" />
              <ApplyBadge desiredGen={15} appliedGen={14} appliedError="" />
              <ApplyBadge desiredGen={7} appliedGen={7} appliedError="time_zone_unset" />
            </div>
            <div className="flex flex-col gap-2">
              <h3 className="text-sm text-muted-foreground">Log severity</h3>
              <SeverityLabel severity="debug" />
              <SeverityLabel severity="info" />
              <SeverityLabel severity="warn" />
              <SeverityLabel severity="error" />
            </div>
            <div className="flex flex-col items-start gap-2">
              <h3 className="text-sm text-muted-foreground">Live feed</h3>
              <FeedStatus feed={{ state: 'live' }} />
              <FeedStatus feed={{ state: 'connecting' }} />
              <FeedStatus feed={{ state: 'reconnecting', inSeconds: 4 }} />
              <FeedStatus feed={{ state: 'idle' }} />
            </div>
          </div>
        </Section>

        <Section title="Tables while empty, loading and failed">
          <div className="grid gap-6 xl:grid-cols-3">
            <DataTable
              caption="Devices, none yet"
              columns={emptyColumns}
              rows={[]}
              rowKey={d => d.id}
              empty={(
                <EmptyState
                  icon={SpeakerIcon}
                  title="No speakers yet"
                  description="Enroll one with a code: the speaker runs a one-line command and shows up here."
                  action={<Button>Enroll a speaker</Button>}
                />
              )}
            />
            <DataTable caption="Devices, loading" columns={emptyColumns} rows={undefined} status="loading" rowKey={d => d.id} empty={null} />
            <DataTable
              caption="Devices, failed"
              columns={emptyColumns}
              rows={undefined}
              status="error"
              rowKey={d => d.id}
              empty={null}
              error={<ErrorState title="Couldn't load the devices" error={new ApiError(503, { error: 'unavailable' })} onRetry={() => {}} />}
            />
          </div>
        </Section>

        <Section title="Actions">
          <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-card p-5">
            <Button size="lg">Save configuration</Button>
            <Button>Enroll a speaker</Button>
            <Button variant="outline">Send command</Button>
            <Button variant="secondary">Test provider</Button>
            <Button variant="ghost">Cancel</Button>
            <Button variant="destructive">Revoke token</Button>
            <Button variant="link">View the log</Button>
            <Button disabled>Saving…</Button>
          </div>
        </Section>

        <Section title="Type">
          <div className="flex flex-col gap-3 rounded-lg border bg-card p-5">
            <p className="text-2xl font-semibold tracking-[-0.01em]">24 · Rare headline numbers</p>
            <p className="text-xl font-semibold">20 · Page title</p>
            <p className="text-lg font-semibold">16 · Section title</p>
            <p className="text-base">14 · Body, forms and descriptions</p>
            <p className="text-sm">13 · Tables, lists and controls</p>
            <p className="text-xs text-muted-foreground">12 · Table headings and captions</p>
            <p className="font-mono text-xs">12 mono · 0193a4c2-7f1e-7c3a-9d52-2b8e41f0a611 · time_zone_unset</p>
            <p className="text-sm">
              <time dateTime="2026-11-12T15:04:12Z" className="font-semibold">21:04:12</time>
              {' '}
              times are bold tabular figures, aligned down a column
            </p>
          </div>
        </Section>
      </div>
    </>
  )
}
