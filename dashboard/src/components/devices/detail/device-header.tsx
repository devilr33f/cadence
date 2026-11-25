import type { Detail } from '@/api/gen'
import { Link } from '@tanstack/react-router'
import { HouseIcon, PencilIcon } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { useFocusReturn } from '@/hooks/use-focus-return'
import { stampText } from '@/lib/format-time'
import { useTimeZone } from '@/lib/time-zone'
import { DeviceStateBadge } from '../device-state'
import { useBoardNames } from '../use-boards'
import { RenameForm } from './rename-form'
import { RoomForm } from './room-form'

// the device's name and state, then its board, when it last connected and its id: each said once, here. the name is
// renamed in place, and Rename takes the focus back once the form goes
export function DeviceHeader({ device }: { device: Detail }) {
  const timeZone = useTimeZone()
  const boards = useBoardNames()
  const board = boards.get(device.model)
  const [renaming, setRenaming] = useState(false)
  const back = useFocusReturn<HTMLButtonElement>(renaming)
  const [placing, setPlacing] = useState(false)
  const roomBack = useFocusReturn<HTMLButtonElement>(placing)
  return (
    <div className="mb-6 flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {renaming
          ? (
              <>
                {/* the page keeps its title while the name is edited */}
                <h1 className="sr-only">{device.name}</h1>
                <RenameForm
                  device={device}
                  onDone={() => {
                    back.arm()
                    setRenaming(false)
                  }}
                />
              </>
            )
          : (
              <>
                <h1 className="text-xl font-semibold">{device.name}</h1>
                <DeviceStateBadge device={device} appearance="pill" />
                <Button ref={back.ref} type="button" variant="ghost" size="sm" aria-label={`Rename ${device.name}`} onClick={() => setRenaming(true)}>
                  <PencilIcon aria-hidden="true" />
                  Rename
                </Button>
              </>
            )}
      </div>
      {placing && (
        <RoomForm
          device={device}
          onDone={() => {
            roomBack.arm()
            setPlacing(false)
          }}
        />
      )}
      <dl className="flex flex-wrap items-center gap-x-5 gap-y-1 text-sm text-muted-foreground">
        {/* a speaker and a screen of one room are one assistant */}
        <div className="flex items-center gap-1.5">
          <dt>Room</dt>
          <dd className="flex items-center gap-1.5 text-foreground">
            {device.room || 'None'}
            {!placing && (
              <Button ref={roomBack.ref} type="button" variant="ghost" size="xs" aria-label={`Set the room of ${device.name}`} onClick={() => setPlacing(true)}>
                <HouseIcon aria-hidden="true" />
                Set
              </Button>
            )}
          </dd>
        </div>
        {device.group && (
          <div className="flex gap-1.5">
            <dt>Group</dt>
            <dd>
              <Link to="/groups/$id" params={{ id: device.group.id }} className="text-foreground underline-offset-4 hover:underline">
                {device.group.name}
              </Link>
            </dd>
          </div>
        )}
        <div className="flex gap-1.5">
          <dt>Board</dt>
          <dd className={board === undefined
            ? 'font-mono text-xs/5 text-foreground'
            : 'text-foreground'}
          >
            {board ?? device.model}
          </dd>
        </div>
        <div className="flex gap-1.5">
          <dt>Last seen</dt>
          <dd className="text-foreground">
            {device.lastSeenAt === null
              ? 'Never'
              : <time dateTime={device.lastSeenAt} className="font-semibold">{stampText(device.lastSeenAt, timeZone)}</time>}
          </dd>
        </div>
        <div className="flex gap-1.5">
          <dt className="sr-only">Traces</dt>
          <dd>
            <Link to="/traces" search={{ device: device.id }} className="text-foreground underline-offset-4 hover:underline">
              Its traces
            </Link>
          </dd>
        </div>
        <div className="flex min-w-0 gap-1.5">
          <dt>Id</dt>
          <dd className="font-mono text-xs/5 break-all text-foreground">{device.id}</dd>
        </div>
      </dl>
    </div>
  )
}
