import type { LucideIcon } from 'lucide-react'
import type { MediaCommands, PlayerCommand } from './use-media-commands'
import type { MediaSession } from '@/api/gen'
import { PauseIcon, PlayIcon, SkipBackIcon, SkipForwardIcon, SquareIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'

type Action = MediaSession['actions'][number]

function Press({ name, label, icon: Icon, commands, primary = false }: {
  name: PlayerCommand
  label: string
  icon: LucideIcon
  commands: MediaCommands
  primary?: boolean
}) {
  const busy = commands.running !== undefined
  return (
    // aria-disabled, never disabled: the focus stays on the button while its command runs
    <Button
      type="button"
      variant={primary
        ? 'outline'
        : 'ghost'}
      size={primary
        ? 'icon-lg'
        : 'icon'}
      aria-label={label}
      aria-disabled={busy || undefined}
      onClick={() => {
        if (!busy)
          void commands.command(name)
      }}
    >
      <Icon aria-hidden="true" />
    </Button>
  )
}

// previous, play or pause, next and stop, each only where the session's actions offer it
export function Transport({ session, commands }: { session: MediaSession, commands: MediaCommands }) {
  const can = (action: Action) => session.actions.includes(action)
  const moving = session.state === 'playing' || session.state === 'buffering'
  const toggle = moving
    ? can('pause') && <Press name="pause" label="Pause" icon={PauseIcon} commands={commands} primary />
    : can('resume') && <Press name="resume" label="Play" icon={PlayIcon} commands={commands} primary />
  if (!toggle && !can('previous') && !can('next') && !can('stop'))
    return null
  return (
    <div role="group" aria-label="Playback" className="flex items-center justify-center gap-1">
      {can('previous') && <Press name="previous" label="Previous" icon={SkipBackIcon} commands={commands} />}
      {toggle}
      {can('next') && <Press name="next" label="Next" icon={SkipForwardIcon} commands={commands} />}
      {can('stop') && <Press name="stop" label="Stop" icon={SquareIcon} commands={commands} />}
    </div>
  )
}
