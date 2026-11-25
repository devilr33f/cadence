import type { GroupDraft } from './group-draft'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { segmentsOf } from '@/components/forms/json-pointer'
import { refusedText } from '@/components/forms/refusal'
import { membersOf } from './group-draft'

export interface GroupRefusal {
  // the field's key, as GroupErrors names it
  field?: string
  message: string
}

const BODY_FIELDS: ReadonlySet<string> = new Set([
  'name',
  'kind',
  'latencyMs',
  'bitrateKbps',
  'waitMs',
  'preferredLeader',
])
const MEMBER_FIELDS: ReadonlySet<string> = new Set(['channel', 'trimMs'])

// where a save's refusal belongs: 409 in_group and 422 unknown_device name a member (/body/members/<i>/deviceId, or
// /body/members/<i>, or the list alone when another save took a member meanwhile), 409 name_taken the name, 422
// invalid any field (a pair's channels as /body/members/1/channel); a member is named by its device's name. 409
// stereo_pair and 404 not_found have no path and go beside the form, as anything else does, in codes
export function groupRefusal(error: unknown, draft: GroupDraft, names: ReadonlyMap<string, string>): GroupRefusal {
  if (!(error instanceof ApiError)) {
    return {
      message: describeError(error),
    }
  }
  if (error.status === 404)
    return { message: `This group is gone: another change deleted it (${error.error}).` }
  if (error.error === 'stereo_pair')
    return { message: `A stereo pair keeps its two speakers (${error.error}).` }
  if (error.path === undefined) {
    return {
      message: describeError(error),
    }
  }

  const [where, key, index, member, ...rest] = segmentsOf(error.path)
  if (where !== 'body' || key === undefined)
    return { message: `The server answered ${error.status} ${error.error} at ${error.path}.` }
  if (key === 'name' && error.error === 'name_taken')
    return { field: 'name', message: `Another group has this name (${error.error}).` }
  if (BODY_FIELDS.has(key) && index === undefined) {
    return {
      field: key,
      message: refusedText(error),
    }
  }

  if (key === 'members' && rest.length === 0) {
    const pair = draft.kind === 'stereo_pair'
    const at = index !== undefined && /^\d+$/.test(index)
      ? Number(index)
      : undefined
    const sent = at === undefined
      ? undefined
      : membersOf(draft)[at]
    if (sent && member !== undefined && MEMBER_FIELDS.has(member)) {
      // a pair's channels are the form's own: a refusal of them is the pair's speakers'
      if (pair && member === 'channel') {
        return {
          field: 'members/1/deviceId',
          message: refusedText(error),
        }
      }
      return {
        field: `members/${at}/${member}`,
        message: refusedText(error),
      }
    }
    if (member === undefined || member === 'deviceId') {
      const field = pair && sent
        ? `members/${at}/deviceId`
        : 'members'
      const name = sent && (names.get(sent.deviceId) ?? sent.deviceId)
      if (error.error === 'in_group') {
        return {
          field,
          message: name
            ? `«${name}» is in another group: take it out there first (${error.error}).`
            : `A device is in another group now (${error.error}).`,
        }
      }
      if (error.error === 'unknown_device') {
        return {
          field,
          message: name
            ? `«${name}» is no longer a device (${error.error}).`
            : `A device is no longer known (${error.error}).`,
        }
      }
      return {
        field,
        message: name
          ? `The server refused «${name}» (${error.error}).`
          : refusedText(error),
      }
    }
  }
  return { message: `The server answered ${error.status} ${error.error} at ${error.path}.` }
}
