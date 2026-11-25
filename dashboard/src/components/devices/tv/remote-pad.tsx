import type { KeyboardEvent, ReactNode } from 'react'
import type { RemoteKey } from './use-key'
import { ArrowDownIcon, ArrowLeftIcon, ArrowRightIcon, ArrowUpIcon, HouseIcon, MenuIcon, PlayIcon, SkipBackIcon, SkipForwardIcon, Undo2Icon, Volume1Icon, VolumeXIcon } from 'lucide-react'
import { useId } from 'react'
import { Button } from '@/components/ui/button'
import { useRemoteKey } from './use-key'

const KEY_LABELS: Readonly<Record<RemoteKey, string>> = {
  up: 'Up',
  down: 'Down',
  left: 'Left',
  right: 'Right',
  ok: 'OK',
  back: 'Back',
  home: 'Home',
  menu: 'Menu',
  play_pause: 'Play or pause',
  previous: 'Previous',
  next: 'Next',
  volume_down: 'Volume down',
  volume_mute: 'Mute',
}

// what a keyboard key sends while the pad has the focus
const KEYBOARD: Readonly<Record<string, RemoteKey>> = {
  ArrowUp: 'up',
  ArrowDown: 'down',
  ArrowLeft: 'left',
  ArrowRight: 'right',
  Enter: 'ok',
  Backspace: 'back',
  Escape: 'back',
}

function Key({ name, icon, onPress, className }: { name: RemoteKey, icon?: ReactNode, onPress: (key: RemoteKey) => void, className?: string }) {
  return (
    <Button
      type="button"
      variant="outline"
      size={icon ? 'icon-lg' : 'lg'}
      aria-label={KEY_LABELS[name]}
      className={className}
      onClick={() => onPress(name)}
    >
      {icon ?? KEY_LABELS[name]}
    </Button>
  )
}

function statusText(result: ReturnType<typeof useRemoteKey>['result']): string {
  switch (result.status) {
    case 'idle':
      return 'Press a key, or use the arrow keys and Enter here.'
    case 'pending':
      return `Sending ${KEY_LABELS[result.key]}…`
    case 'sent':
      return `${KEY_LABELS[result.key]} sent.`
    case 'failed':
      return `${KEY_LABELS[result.key]} failed. ${result.message}`
  }
}

// the tv's remote: the d-pad and the keys around it, each sent as input.remote key, as if pressed on the remote
export function RemotePad({ deviceId }: { deviceId: string }) {
  const { result, press } = useRemoteKey(deviceId)
  const statusId = useId()
  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const key = KEYBOARD[event.key]
    // a button's own Enter is its click
    if (!key || (event.key === 'Enter' && event.target instanceof HTMLButtonElement))
      return
    event.preventDefault()
    void press(key)
  }
  const send = (key: RemoteKey) => void press(key)
  return (
    <div className="flex flex-col gap-3">
      <div
        role="group"
        aria-label="Remote pad"
        aria-describedby={statusId}
        tabIndex={0}
        onKeyDown={onKeyDown}
        className="flex flex-col items-center gap-3 rounded-md p-2"
      >
        <div className="grid grid-cols-3 gap-1.5">
          <span />
          <Key name="up" icon={<ArrowUpIcon aria-hidden="true" />} onPress={send} />
          <span />
          <Key name="left" icon={<ArrowLeftIcon aria-hidden="true" />} onPress={send} />
          <Key name="ok" onPress={send} className="size-9 px-0 font-semibold" />
          <Key name="right" icon={<ArrowRightIcon aria-hidden="true" />} onPress={send} />
          <span />
          <Key name="down" icon={<ArrowDownIcon aria-hidden="true" />} onPress={send} />
          <span />
        </div>
        <div className="flex flex-wrap justify-center gap-1.5">
          <Key name="back" icon={<Undo2Icon aria-hidden="true" />} onPress={send} />
          <Key name="home" icon={<HouseIcon aria-hidden="true" />} onPress={send} />
          <Key name="menu" icon={<MenuIcon aria-hidden="true" />} onPress={send} />
        </div>
        <div className="flex flex-wrap justify-center gap-1.5">
          <Key name="previous" icon={<SkipBackIcon aria-hidden="true" />} onPress={send} />
          <Key name="play_pause" icon={<PlayIcon aria-hidden="true" />} onPress={send} />
          <Key name="next" icon={<SkipForwardIcon aria-hidden="true" />} onPress={send} />
          <Key name="volume_down" icon={<Volume1Icon aria-hidden="true" />} onPress={send} />
          <Key name="volume_mute" icon={<VolumeXIcon aria-hidden="true" />} onPress={send} />
        </div>
      </div>
      <p
        id={statusId}
        role={result.status === 'failed'
          ? 'alert'
          : 'status'}
        className={result.status === 'failed'
          ? 'text-sm text-destructive'
          : 'text-sm text-muted-foreground'}
      >
        {statusText(result)}
      </p>
    </div>
  )
}
