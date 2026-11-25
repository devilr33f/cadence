import type { GroupDraft, GroupErrors, MemberDraft } from './group-draft'
import type { DeviceView } from '@/api/gen'
import { FieldMessage, RequiredMark } from '@/components/forms/fields/field-shell'
import { describedBy } from '@/components/forms/fields/field-text'
import { FormField } from '@/components/forms/form-field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { CHANNELS, MAX_MEMBERS, MIN_MEMBERS, TRIM_MAX_MS, TRIM_MIN_MS } from './group-draft'
import { CHANNEL_LABELS, elsewhere } from './group-words'

function TrimInput({ id, label, value, error, onChange }: {
  id: string
  label: string
  value: string
  error?: string
  onChange: (text: string) => void
}) {
  return (
    <Input
      id={id}
      className="w-24"
      inputMode="numeric"
      placeholder="0"
      aria-label={label}
      aria-invalid={error ? true : undefined}
      aria-describedby={describedBy(id, { error })}
      value={value}
      onChange={e => onChange(e.target.value)}
    />
  )
}

// a group's devices: each enrolled one with a box, and a picked one's channel and trim beside it
export function GroupMembersField({ baseId, devices, groupId, draft, errors, onPick, onMember }: {
  baseId: string
  devices: readonly DeviceView[]
  groupId?: string
  draft: GroupDraft
  errors: GroupErrors
  onPick: (deviceId: string, picked: boolean) => void
  onMember: (index: number, member: MemberDraft) => void
}) {
  const id = `${baseId}-members`
  const error = errors.members
  return (
    <fieldset
      className="flex flex-col gap-3"
      data-invalid={error ? true : undefined}
      aria-describedby={describedBy(id, { description: true, error })}
    >
      <legend className="mb-1 text-sm font-medium">
        Devices
        <RequiredMark required />
      </legend>
      <p id={`${id}-description`} className="-mt-1 text-sm text-muted-foreground">
        {`${MIN_MEMBERS} to ${MAX_MEMBERS} devices, each playing the channel set beside it. Trim delays one device against the rest, ${TRIM_MIN_MS} to ${TRIM_MAX_MS} ms, for a speaker farther away.`}
      </p>
      {devices.length === 0 && <p className="text-sm text-muted-foreground">No enrolled devices.</p>}
      <ul className="flex flex-col gap-2">
        {devices.map((device) => {
          const at = draft.members.findIndex(m => m.deviceId === device.id)
          const member = draft.members[at]
          const other = elsewhere(device, groupId)
          const boxId = `${id}-${device.id}`
          const trimId = `${baseId}-members-${at}-trimMs`
          const trimError = errors[`members/${at}/trimMs`]
          return (
            <li key={device.id} className="flex flex-col gap-1">
              <div className="flex min-h-8 flex-wrap items-center gap-x-3 gap-y-2">
                <input
                  id={boxId}
                  type="checkbox"
                  className="size-4 shrink-0"
                  checked={member !== undefined}
                  disabled={other !== undefined && member === undefined}
                  aria-describedby={other === undefined
                    ? undefined
                    : `${boxId}-elsewhere`}
                  onChange={e => onPick(device.id, e.target.checked)}
                />
                <label htmlFor={boxId} className="min-w-32 text-sm font-medium">{device.name}</label>
                {other !== undefined && (
                  <span id={`${boxId}-elsewhere`} className="text-sm text-muted-foreground">{`In ${other}`}</span>
                )}
                {member && (
                  <>
                    <NativeSelect
                      aria-label={`Channel of ${device.name}`}
                      value={member.channel}
                      onChange={e => onMember(at, { ...member, channel: e.target.value as MemberDraft['channel'] })}
                    >
                      {CHANNELS.map(c => <NativeSelectOption key={c} value={c}>{CHANNEL_LABELS[c]}</NativeSelectOption>)}
                    </NativeSelect>
                    <TrimInput
                      id={trimId}
                      label={`Trim of ${device.name}, ms`}
                      value={member.trimMs}
                      error={trimError}
                      onChange={text => onMember(at, { ...member, trimMs: text })}
                    />
                  </>
                )}
              </div>
              {member && <FieldMessage id={trimId} error={trimError} />}
            </li>
          )
        })}
      </ul>
      <FieldMessage id={id} error={error} />
    </fieldset>
  )
}

// a stereo pair: one speaker plays left, the other right; both are best the same model, so they sound alike
export function StereoPairFields({ baseId, devices, groupId, draft, errors, onSide }: {
  baseId: string
  devices: readonly DeviceView[]
  groupId?: string
  draft: GroupDraft
  errors: GroupErrors
  onSide: (side: 'left' | 'right', member: MemberDraft) => void
}) {
  const models = new Map(devices.map(d => [d.id, d.model]))
  const leftModel = models.get(draft.left.deviceId)
  const rightModel = models.get(draft.right.deviceId)
  const mismatch = leftModel !== undefined && rightModel !== undefined && leftModel !== rightModel
  const sides = [
    {
      side: 'left',
      index: 0,
      label: 'Left speaker',
      member: draft.left,
    },
    {
      side: 'right',
      index: 1,
      label: 'Right speaker',
      member: draft.right,
    },
  ] as const
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        Two speakers, one playing the left channel and one the right, held within 0.1 ms of each other. Pick two of the
        same model, so both sides sound alike.
      </p>
      {sides.map(({ side, index, label, member }) => {
        const id = `${baseId}-members-${index}-deviceId`
        const error = errors[`members/${index}/deviceId`]
        const trimId = `${baseId}-members-${index}-trimMs`
        const trimError = errors[`members/${index}/trimMs`]
        return (
          <div key={side} className="flex flex-col gap-2">
            <FormField id={id} label={label} required error={error}>
              <NativeSelect
                id={id}
                className="w-full sm:w-72"
                value={member.deviceId}
                aria-required
                aria-invalid={error ? true : undefined}
                aria-describedby={describedBy(id, { error })}
                onChange={e => onSide(side, { ...member, deviceId: e.target.value })}
              >
                <NativeSelectOption value="">Pick a speaker</NativeSelectOption>
                {devices.map((device) => {
                  const other = elsewhere(device, groupId)
                  return (
                    <NativeSelectOption key={device.id} value={device.id} disabled={other !== undefined}>
                      {other === undefined
                        ? device.name
                        : `${device.name} (in ${other})`}
                    </NativeSelectOption>
                  )
                })}
              </NativeSelect>
            </FormField>
            {member.deviceId !== '' && (
              <div className="flex flex-col gap-1">
                <div className="flex items-center gap-2">
                  <TrimInput
                    id={trimId}
                    label={`Trim of the ${side} speaker, ms`}
                    value={member.trimMs}
                    error={trimError}
                    onChange={text => onSide(side, { ...member, trimMs: text })}
                  />
                  <span className="text-sm text-muted-foreground">ms trim</span>
                </div>
                <FieldMessage id={trimId} error={trimError} />
              </div>
            )}
          </div>
        )
      })}
      {mismatch && (
        <p role="status" className="text-sm text-warning">
          The two speakers are different models: the pair works, but the sides may not sound alike.
        </p>
      )}
    </div>
  )
}
