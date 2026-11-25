import type { GroupTarget } from './group-dialog'
import type { CalibrationView, DeviceView, GroupView } from '@/api/gen'
import type { Topic } from '@/lib/live/topics'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { ChevronLeftIcon, GroupIcon, PencilIcon } from 'lucide-react'
import { useId, useMemo, useRef, useState } from 'react'
import { ApiError } from '@/api/error'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { KeyValueList } from '@/components/data/key-value-list'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { devicesQueryOptions, isEnrolled } from '@/lib/devices'
import { groupQueryOptions, groupsQueryOptions, useGroupActions } from '@/lib/groups'
import { useLiveTopics } from '@/lib/live/use-live'
import { CalibrateDialog } from './calibrate-dialog'
import { boostList } from './calibration-retry'
import { GroupActivity } from './group-activity'
import { GroupCalibration } from './group-calibration'
import { GroupDialog } from './group-dialog'
import { GroupMembers } from './group-members'
import { GroupTracking } from './group-tracking'
import { KIND_LABELS, leaderLine, tuned } from './group-words'

// group_state carries each member's status, group_changed the membership and the leader, devices the online flags
const TOPICS: readonly Topic[] = ['groups', 'devices']
const NO_DEVICES: DeviceView[] = []

function BackLink() {
  return (
    <Link to="/groups" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ChevronLeftIcon aria-hidden="true" className="size-4" />
      Groups
    </Link>
  )
}

export function GroupPage() {
  const { id } = useParams({ from: '/_authed/groups/$id' })
  useLiveTopics(TOPICS)
  return <GroupScreen key={id} id={id} />
}

function GroupScreen({ id }: { id: string }) {
  const api = useApi()
  const detail = useQuery(groupQueryOptions(api, id))

  if (detail.isPending) {
    return (
      <>
        <BackLink />
        <LoadingLines lines={4} label="Loading the group" />
      </>
    )
  }
  if (detail.isError) {
    return (
      <>
        <BackLink />
        <div className="rounded-lg border bg-card">
          {detail.error instanceof ApiError && detail.error.status === 404
            ? <EmptyState icon={GroupIcon} title="This group is gone" description="It was deleted, here or by Home Assistant, or never was." />
            : <ErrorState title="This group could not be shown" error={detail.error} onRetry={() => void detail.refetch()} />}
        </div>
      </>
    )
  }
  return <GroupDetail group={detail.data} />
}

function GroupDetail({ group }: { group: GroupView }) {
  const api = useApi()
  const navigate = useNavigate()
  const devices = useQuery(devicesQueryOptions(api))
  const groups = useQuery(groupsQueryOptions(api))
  const actions = useGroupActions()
  const [target, setTarget] = useState<GroupTarget>()
  const [calibration, setCalibration] = useState<CalibrationView>()
  // the louder chirps a retry asked for, by member: this page's alone, reset by a fresh run
  const [boosts, setBoosts] = useState<Record<string, number>>({})
  const editRef = useRef<HTMLButtonElement>(null)
  const membersTitleId = useId()

  const all = devices.data ?? NO_DEVICES
  const enrolled = useMemo(() => all.filter(isEnrolled), [all])
  const names = useMemo(() => new Map(all.map(d => [d.id, d.name])), [all])
  const nameOf = (deviceId: string) => names.get(deviceId) ?? deviceId
  const ready = devices.data !== undefined && groups.data !== undefined

  return (
    <>
      <BackLink />
      <div className="mb-6 flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-xl font-semibold break-words">{group.name}</h1>
            <GroupActivity group={group} appearance="pill" />
          </div>
          <p className="mt-1 text-sm text-muted-foreground">{`${KIND_LABELS[group.kind]} · ${group.members.length} members`}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            ref={editRef}
            type="button"
            variant="outline"
            aria-disabled={!ready || undefined}
            onClick={() => {
              if (ready)
                setTarget({ kind: 'edit', group })
            }}
          >
            <PencilIcon aria-hidden="true" />
            Edit
          </Button>
          <CalibrateDialog
            groupName={group.name}
            onCalibrate={() => {
              setBoosts({})
              return actions.calibrate(group.id)
            }}
            onListen={(onPhase) => {
              setBoosts({})
              return actions.calibrateByListening(group.id, onPhase)
            }}
            onDone={setCalibration}
          />
          <ConfirmDialog
            trigger={<Button type="button" variant="outline">Delete</Button>}
            title={`Delete ${group.name}?`}
            description="Its devices stop playing together and each goes back to playing on its own."
            confirmLabel="Delete the group"
            tone="destructive"
            inlineError
            onConfirm={async () => {
              await actions.remove(group.id)
              await navigate({ to: '/groups' })
            }}
          />
        </div>
      </div>
      <div className="flex max-w-(--content-max) flex-col gap-8">
        <KeyValueList
          items={[
            {
              label: 'Leader',
              value: leaderLine(group, nameOf),
            },
            {
              label: 'Preferred leader',
              value: group.preferredLeader === ''
                ? 'Any member'
                : nameOf(group.preferredLeader),
            },
            {
              label: 'Latency',
              value: <span className="tabular-nums">{tuned(group.tuning?.latencyMs ?? group.latencyMs, 'ms', group.autoLatency)}</span>,
            },
            {
              label: 'Stream',
              value: group.pcm
                ? 'Uncompressed PCM'
                : <span className="tabular-nums">{`Opus, ${tuned(group.tuning?.bitrateKbps || group.bitrateKbps, 'kbit/s', group.autoBitrate)}`}</span>,
            },
            {
              label: 'Start wait',
              value: <span className="tabular-nums">{tuned(group.tuning?.waitMs || group.waitMs, 'ms', group.autoWait)}</span>,
            },
          ]}
        />
        <GroupTracking group={group} names={names} onSave={actions.save} />
        <section aria-labelledby={membersTitleId} className="flex flex-col gap-3">
          <h2 id={membersTitleId} className="text-lg font-semibold">Members</h2>
          <GroupMembers group={group} names={names} />
        </section>
        {calibration && (
          <GroupCalibration
            calibration={calibration}
            names={names}
            boosts={boosts}
            onRetry={(next, onPhase) => {
              // the listener's microphone starts inside the press that called this, before any await
              const run = calibration.mode === 'listener'
                ? actions.calibrateByListening(group.id, onPhase, boostList(next))
                : actions.calibrate(group.id, boostList(next))
              return run.then((result) => {
                setBoosts(next)
                setCalibration(result)
              })
            }}
          />
        )}
      </div>
      <GroupDialog
        target={target}
        onClose={() => setTarget(undefined)}
        finalFocus={editRef}
        devices={enrolled}
        groups={groups.data ?? []}
        onSave={actions.save}
      />
    </>
  )
}
