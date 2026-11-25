import type { FormEvent } from 'react'
import type { ApproveOutBody } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { CircleCheckIcon, LoaderCircleIcon, TimerOffIcon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { useApi } from '@/api/use-api'
import { PageHeader } from '@/components/data/page-header'
import { ErrorState, LoadingLines } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { claimCodeText, claimQueryOptions, normalizeClaimCode, useClaimActions } from '@/lib/claims'
import { nameError, roomError } from '../devices/device-name'
import { RoomInput } from '../devices/room-field'
import { useBoardNames } from '../devices/use-boards'
import { ClaimCountdown } from './claim-countdown'

type Field = 'name' | 'room'

interface Refusal {
  field?: Field
  message: string
  // the claim is gone: nothing more to answer here
  gone?: boolean
}

function refusalOf(failure: unknown): Refusal {
  if (failure instanceof ApiError) {
    if (failure.status === 404)
      return { gone: true, message: 'This code is no longer open: it expired, or was approved or declined meanwhile.' }
    if (failure.status === 409 && failure.error === 'name_taken')
      return { field: 'name', message: 'Another device has this name, in any case (name_taken).' }
    if (failure.status === 422) {
      return {
        field: failure.path === '/body/room'
          ? 'room'
          : 'name',
        message: 'The server refused this value (invalid).',
      }
    }
  }
  return {
    message: describeError(failure),
  }
}

function Gone({ message }: { message: string }) {
  return (
    <div role="alert" className="flex max-w-xl flex-col items-start gap-3 rounded-md border bg-warning-surface p-4">
      <p className="flex items-center gap-2 font-medium text-warning">
        <TimerOffIcon aria-hidden="true" className="size-4" />
        No screen waits with this code
      </p>
      <p className="text-sm">{message}</p>
      <p className="text-sm">A screen whose code ran out shows a new one by itself.</p>
      <Link to="/claims" className="text-sm underline underline-offset-4">See the screens waiting</Link>
    </div>
  )
}

function Approved({ device }: { device: ApproveOutBody }) {
  return (
    <div role="status" aria-label="Approved" className="flex max-w-xl flex-col items-start gap-3 rounded-md border p-4">
      <p className="flex items-center gap-2 font-medium text-success">
        <CircleCheckIcon aria-hidden="true" className="size-4" />
        Approved — the screen connects by itself
      </p>
      <p className="text-sm">
        {device.room
          ? `${device.name} joins the room ${device.room}. Nothing to press on the TV.`
          : `${device.name} joins without a room. Nothing to press on the TV.`}
      </p>
      <Link to="/devices/$id" params={{ id: device.id }} className="text-sm underline underline-offset-4">{`Open ${device.name}`}</Link>
    </div>
  )
}

// the page a screen's qr opens: its code as the screen shows it, then a name and a room for it; approving creates the
// device, and the screen's own poll enrols it
export function ClaimPage() {
  const params = useParams({ from: '/_authed/claims/$code' })
  const code = normalizeClaimCode(params.code)
  const api = useApi()
  const boards = useBoardNames()
  const claim = useQuery({
    ...claimQueryOptions(api, code),
    retry: false,
  })
  const actions = useClaimActions()
  const id = useId()
  const nameRef = useRef<HTMLInputElement>(null)
  const roomRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [room, setRoom] = useState('')
  const [pending, setPending] = useState<'approve' | 'decline'>()
  const [refusal, setRefusal] = useState<Refusal>()
  const [approved, setApproved] = useState<ApproveOutBody>()
  const [declined, setDeclined] = useState(false)

  const title = `Screen ${claimCodeText(code)}`
  if (approved) {
    return (
      <>
        <PageHeader title={title} />
        <Approved device={approved} />
      </>
    )
  }
  if (declined) {
    return (
      <>
        <PageHeader title={title} />
        <div role="status" aria-label="Declined" className="flex max-w-xl flex-col items-start gap-2 rounded-md border p-4">
          <p className="font-medium">Declined</p>
          <p className="text-sm">The screen says so and shows a new code.</p>
          <Link to="/claims" className="text-sm underline underline-offset-4">See the screens waiting</Link>
        </div>
      </>
    )
  }
  if (claim.isPending)
    return <LoadingLines lines={3} label="Loading the screen's claim" />
  if (claim.isError) {
    return (
      <>
        <PageHeader title={title} />
        {claim.error instanceof ApiError && claim.error.status === 404
          ? <Gone message="Its code is unknown, ran out, or was approved or declined already." />
          : <ErrorState title="The claim could not be read" error={claim.error} onRetry={() => void claim.refetch()} />}
      </>
    )
  }
  if (refusal?.gone) {
    return (
      <>
        <PageHeader title={title} />
        <Gone message={refusal.message} />
      </>
    )
  }

  const board = boards.get(claim.data.board) ?? claim.data.board

  async function approve(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    const trimmed = name.trim()
    const invalid = nameError(trimmed)
    if (invalid) {
      setRefusal({ field: 'name', message: invalid })
      nameRef.current?.focus()
      return
    }
    const where = room.trim()
    const badRoom = roomError(where)
    if (badRoom) {
      setRefusal({ field: 'room', message: badRoom })
      roomRef.current?.focus()
      return
    }
    setPending('approve')
    setRefusal(undefined)
    try {
      setApproved(await actions.approve(code, trimmed, where))
    }
    catch (failure) {
      const refused = refusalOf(failure)
      setRefusal(refused)
      if (refused.field === 'name')
        nameRef.current?.focus()
      if (refused.field === 'room')
        roomRef.current?.focus()
    }
    finally {
      setPending(undefined)
    }
  }

  async function decline() {
    if (pending)
      return
    setPending('decline')
    setRefusal(undefined)
    try {
      await actions.decline(code)
      setDeclined(true)
    }
    catch (failure) {
      setRefusal(refusalOf(failure))
    }
    finally {
      setPending(undefined)
    }
  }

  const describedBy = (field: Field) => refusal?.field === field
    ? `${id}-error`
    : undefined
  return (
    <>
      <PageHeader
        title={title}
        description="A screen asks to join Cadence. Check that this code is the one it shows, then name it and put it in a room."
      />
      <div className="flex max-w-xl flex-col gap-6">
        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Code</dt>
          <dd className="font-mono text-2xl font-semibold tracking-wider">{claimCodeText(code)}</dd>
          <dt className="text-muted-foreground">Board</dt>
          <dd>{board}</dd>
          <dt className="text-muted-foreground">From</dt>
          <dd className="font-mono text-xs/5">{claim.data.source}</dd>
          <dt className="text-muted-foreground">Code good for</dt>
          <dd><ClaimCountdown expiresAt={claim.data.expiresAt} /></dd>
        </dl>
        <form method="post" noValidate autoComplete="off" onSubmit={e => void approve(e)} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-name`}>Name</Label>
            <Input
              ref={nameRef}
              id={`${id}-name`}
              value={name}
              required
              autoFocus
              placeholder="Living room TV"
              aria-invalid={refusal?.field === 'name' || undefined}
              aria-describedby={describedBy('name')}
              onChange={e => setName(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-room`}>Room</Label>
            <RoomInput
              ref={roomRef}
              id={`${id}-room`}
              value={room}
              placeholder="No room"
              aria-invalid={refusal?.field === 'room' || undefined}
              aria-describedby={describedBy('room') ?? `${id}-room-hint`}
              onChange={e => setRoom(e.target.value)}
            />
            <p id={`${id}-room-hint`} className="text-sm text-muted-foreground">A speaker in the same room listens for it, and its turns show on this screen.</p>
          </div>
          {refusal && <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{refusal.message}</p>}
          <div className="flex flex-wrap gap-2">
            <Button type="submit" size="lg" aria-disabled={pending !== undefined || undefined}>
              {pending === 'approve' && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
              Approve the screen
            </Button>
            <Button type="button" size="lg" variant="ghost" aria-disabled={pending !== undefined || undefined} onClick={() => void decline()}>
              {pending === 'decline' && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
              Decline
            </Button>
          </div>
        </form>
      </div>
    </>
  )
}
