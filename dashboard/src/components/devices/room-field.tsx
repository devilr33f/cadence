import type { ComponentProps } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { useApi } from '@/api/use-api'
import { Input } from '@/components/ui/input'
import { devicesQueryOptions, knownRooms } from '@/lib/devices'

// a room's name, free text, suggesting the rooms the household's devices are already in: a speaker and a screen of one
// room are one assistant only when their rooms are spelled the same
export function RoomInput(props: ComponentProps<'input'>) {
  const api = useApi()
  const listId = useId()
  const devices = useQuery(devicesQueryOptions(api))
  const rooms = knownRooms(devices.data)
  return (
    <>
      <Input list={listId} autoComplete="off" {...props} />
      <datalist id={listId}>
        {rooms.map(room => <option key={room} value={room} />)}
      </datalist>
    </>
  )
}
