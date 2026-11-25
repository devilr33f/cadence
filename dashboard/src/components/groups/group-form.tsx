import type { FormEvent } from 'react'
import type { GroupDraft, GroupErrors, GroupKind, MemberDraft } from './group-draft'
import type { DeviceView, GroupRequest, GroupView } from '@/api/gen'
import { LoaderCircleIcon } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { Choice } from '@/components/forms/choice'
import { describedBy } from '@/components/forms/fields/field-text'
import { focusFirstInvalid } from '@/components/forms/focus'
import { FormField } from '@/components/forms/form-field'
import { Button } from '@/components/ui/button'
import { DialogClose, DialogFooter } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { BITRATE_MAX_KBPS, BITRATE_MIN_KBPS, checkDraft, draftOf, LATENCY_MAX_MS, LATENCY_MIN_MS, membersOf, NEW_GROUP, requestOf, toggleMember, WAIT_MAX_MS } from './group-draft'
import { GroupMembersField, StereoPairFields } from './group-members-field'
import { groupRefusal } from './group-refusal'
import { KIND_LABELS } from './group-words'

const KINDS: readonly { kind: GroupKind, description: string }[] = [
  { kind: 'group', description: 'Two to eight devices playing the same music in step, each its own channel.' },
  { kind: 'stereo_pair', description: 'Two speakers as one stereo speaker: one left, one right.' },
]

// a member's fields go with any change of the members: their indices move
function withoutMemberErrors(errors: GroupErrors): GroupErrors {
  return Object.fromEntries(Object.entries(errors).filter(([key]) => key !== 'members' && !key.startsWith('members/')))
}

// every field of a group; an update replaces it whole, so what the form does not show (none of it) is sent as read
export function GroupForm({ group, devices, taken, onSave, onDone, onPendingChange }: {
  group?: GroupView
  devices: readonly DeviceView[]
  taken: readonly string[]
  onSave: (id: string | undefined, body: GroupRequest) => Promise<unknown>
  onDone: () => void
  onPendingChange: (pending: boolean) => void
}) {
  const baseId = useId()
  const formRef = useRef<HTMLFormElement>(null)
  const [draft, setDraft] = useState<GroupDraft>(() => group
    ? draftOf(group)
    : NEW_GROUP)
  const [errors, setErrors] = useState<GroupErrors>({})
  const [formError, setFormError] = useState<string>()
  const [pending, setPending] = useState(false)
  const [focusTick, setFocusTick] = useState(0)
  const names = new Map(devices.map(d => [d.id, d.name]))

  useEffect(() => {
    if (focusTick > 0)
      focusFirstInvalid(formRef.current)
  }, [focusTick])

  const change = <K extends keyof GroupDraft>(key: K, value: GroupDraft[K]) => {
    setDraft(current => ({ ...current, [key]: value }))
    setErrors(({ [key]: _, ...rest }) => rest)
  }
  const changeMembers = (next: (current: GroupDraft) => GroupDraft) => {
    setDraft(next)
    setErrors(withoutMemberErrors)
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const found = checkDraft(draft, taken)
    setErrors(found)
    setFormError(undefined)
    if (Object.keys(found).length > 0) {
      setFocusTick(n => n + 1)
      return
    }
    setPending(true)
    onPendingChange(true)
    try {
      await onSave(group?.id, requestOf(draft))
      onDone()
    }
    catch (error) {
      const refusal = groupRefusal(error, draft, names)
      if (refusal.field)
        setErrors({ [refusal.field]: refusal.message })
      else
        setFormError(refusal.message)
      setFocusTick(n => n + 1)
    }
    finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  const id = (field: string) => `${baseId}-${field}`
  const control = (field: string, description = false) => ({
    'id': id(field),
    'aria-invalid': errors[field] ? true : undefined,
    'aria-describedby': describedBy(id(field), { description, error: errors[field] }),
  })
  const members = membersOf(draft).filter(m => m.deviceId !== '')
  return (
    <form method="post" ref={formRef} noValidate autoComplete="off" onSubmit={e => void submit(e)} className="flex flex-col gap-5">
      <FormField id={id('name')} label="Name" required description="The agent finds the group by it: «play jazz in the kitchen»." error={errors.name}>
        <Input {...control('name', true)} value={draft.name} aria-required onChange={e => change('name', e.target.value)} />
      </FormField>
      <fieldset className="flex flex-col gap-2" data-invalid={errors.kind ? true : undefined}>
        <legend className="mb-1 text-sm font-medium">Kind</legend>
        {KINDS.map(({ kind, description }) => (
          <Choice
            key={kind}
            id={id(`kind-${kind}`)}
            type="radio"
            name={id('kind')}
            checked={draft.kind === kind}
            label={KIND_LABELS[kind]}
            description={description}
            onChange={() => changeMembers(current => ({ ...current, kind }))}
          />
        ))}
        {errors.kind && <p role="alert" className="text-sm text-destructive">{errors.kind}</p>}
      </fieldset>
      {draft.kind === 'stereo_pair'
        ? (
            <StereoPairFields
              baseId={baseId}
              devices={devices}
              groupId={group?.id}
              draft={draft}
              errors={errors}
              onSide={(side, member: MemberDraft) => changeMembers(current => ({ ...current, [side]: member }))}
            />
          )
        : (
            <GroupMembersField
              baseId={baseId}
              devices={devices}
              groupId={group?.id}
              draft={draft}
              errors={errors}
              onPick={(deviceId, picked) => changeMembers(current => toggleMember(current, deviceId, picked))}
              onMember={(index, member) => changeMembers(current => ({
                ...current,
                members: current.members.map((m, i) => i === index
                  ? member
                  : m),
              }))}
            />
          )}
      <FormField
        id={id('latencyMs')}
        label={draft.autoLatency ? 'Latency, start at, ms' : 'Latency, ms'}
        required
        description={`How far behind the leader the group plays, ${LATENCY_MIN_MS} to ${LATENCY_MAX_MS}. Lower reacts sooner to a pause or a skip; higher rides out weak Wi-Fi.`}
        error={errors.latencyMs}
      >
        <Input {...control('latencyMs', true)} className="w-full sm:w-40" inputMode="numeric" value={draft.latencyMs} aria-required onChange={e => change('latencyMs', e.target.value)} />
      </FormField>
      <Choice
        id={id('autoLatency')}
        checked={draft.autoLatency}
        label="Tune the latency automatically"
        description="The leader raises it when a member falls behind and lowers it while every member keeps a margin."
        onChange={on => change('autoLatency', on)}
      />
      <FormField id={id('preferredLeader')} label="Leader" description="The member that streams to the rest while it is online; else the first online member." error={errors.preferredLeader}>
        <NativeSelect
          {...control('preferredLeader', true)}
          className="w-full sm:w-72"
          value={members.some(m => m.deviceId === draft.preferredLeader)
            ? draft.preferredLeader
            : ''}
          onChange={e => change('preferredLeader', e.target.value)}
        >
          <NativeSelectOption value="">Any member</NativeSelectOption>
          {members.map(m => <NativeSelectOption key={m.deviceId} value={m.deviceId}>{names.get(m.deviceId) ?? m.deviceId}</NativeSelectOption>)}
        </NativeSelect>
      </FormField>
      <FormField
        id={id('bitrateKbps')}
        label={draft.autoBitrate && !draft.pcm
          ? 'Bitrate, at most, kbit/s'
          : 'Bitrate, kbit/s'}
        description={`The stream's Opus bitrate, ${BITRATE_MIN_KBPS} to ${BITRATE_MAX_KBPS}; empty keeps 256.`}
        error={errors.bitrateKbps}
      >
        <Input {...control('bitrateKbps', true)} className="w-full sm:w-40" inputMode="numeric" value={draft.bitrateKbps} onChange={e => change('bitrateKbps', e.target.value)} />
      </FormField>
      <Choice
        id={id('autoBitrate')}
        checked={draft.autoBitrate}
        label="Tune the bitrate automatically"
        description="The leader steps it down while a member falls behind, and back up once it keeps up. Not with uncompressed PCM."
        onChange={on => change('autoBitrate', on)}
      />
      <FormField id={id('waitMs')} label={draft.autoWait ? 'Start wait, at most, ms' : 'Start wait, ms'} description={`How long a start waits for every member, 0 to ${WAIT_MAX_MS}; empty keeps 3000.`} error={errors.waitMs}>
        <Input {...control('waitMs', true)} className="w-full sm:w-40" inputMode="numeric" value={draft.waitMs} onChange={e => change('waitMs', e.target.value)} />
      </FormField>
      <Choice
        id={id('autoWait')}
        checked={draft.autoWait}
        label="Tune the start wait automatically"
        description="The leader waits as long as the members have lately taken to be ready."
        onChange={on => change('autoWait', on)}
      />
      <Choice
        id={id('pcm')}
        checked={draft.pcm}
        label="Uncompressed"
        description="Stream PCM instead of Opus: more Wi-Fi, no encoding."
        onChange={pcm => change('pcm', pcm)}
      />
      <Choice
        id={id('followVideo')}
        checked={draft.followVideo}
        label="Screens show the video together"
        description="A video started on one of the group's screens opens on its other screens too, muted, their picture in step with the group's sound."
        onChange={on => change('followVideo', on)}
      />
      {formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}
      <DialogFooter>
        <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>Cancel</DialogClose>
        <Button type="submit" aria-disabled={pending || undefined}>
          {pending && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
          {group ? 'Save the group' : 'Add the group'}
        </Button>
      </DialogFooter>
    </form>
  )
}
