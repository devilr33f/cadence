import type { CalibrationView } from '@/api/gen'
import type { ListenerPhase } from '@/lib/listener-calibration'
import { AudioWaveformIcon, Loader2Icon } from 'lucide-react'
import { useId, useState } from 'react'

import { Choice } from '@/components/forms/choice'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { listenerSupported } from '@/lib/listener-recorder'
import { failureText, PHASES } from './calibration-retry'
import { CALIBRATE_DESCRIPTION } from './group-calibration'

export type CalibrateMode = 'listener' | 'mics'

const LISTENER_DESCRIPTION = 'Each speaker plays a short chirp in turn, a few rounds, quieter than its own volume, and '
  + 'this page records them where you stand (up to about 40 seconds). The recording stays in this page and is dropped '
  + 'once measured; only the timings are sent. Allow the microphone when asked, and keep still and quiet.'

const UNSUPPORTED = 'Needs a microphone, and the dashboard over https.'

// a calibration, after asking how to listen: this browser where the owner stands (recommended), or the speakers' own
// microphones. nothing sounds before the owner confirms (rule 7)
export function CalibrateDialog({ groupName, onCalibrate, onListen, onDone }: {
  groupName: string
  onCalibrate: () => Promise<CalibrationView>
  onListen: (onPhase: (phase: ListenerPhase) => void) => Promise<CalibrationView>
  onDone: (calibration: CalibrationView) => void
}) {
  const baseId = useId()
  const [open, setOpen] = useState(false)
  const [supported, setSupported] = useState(false)
  const [mode, setMode] = useState<CalibrateMode>('mics')
  const [pending, setPending] = useState(false)
  const [phase, setPhase] = useState<ListenerPhase>()
  const [error, setError] = useState<string>()

  async function start() {
    if (pending)
      return
    setPending(true)
    setError(undefined)
    setPhase(undefined)
    try {
      // the listener's recording starts inside this press
      const result = mode === 'listener'
        ? await onListen(setPhase)
        : await onCalibrate()
      onDone(result)
      setOpen(false)
    }
    catch (failure) {
      setError(failureText(failure))
    }
    finally {
      setPending(false)
      setPhase(undefined)
    }
  }

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (pending)
          return
        if (next) {
          const can = listenerSupported()
          setSupported(can)
          setMode(can ? 'listener' : 'mics')
        }
        setOpen(next)
        setError(undefined)
      }}
    >
      <AlertDialogTrigger render={(
        <Button type="button" variant="outline">
          <AudioWaveformIcon aria-hidden="true" />
          Calibrate
        </Button>
      )}
      />
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{`Calibrate ${groupName}?`}</AlertDialogTitle>
          <AlertDialogDescription>
            Every speaker plays a short chirp, so each one&apos;s delay can be trimmed to the others. Pause the
            group&apos;s music first.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <fieldset className="flex flex-col gap-3" disabled={pending}>
          <legend className="mb-1 text-sm font-medium">What listens</legend>
          <Choice
            id={`${baseId}-listener`}
            type="radio"
            name={`${baseId}-mode`}
            checked={mode === 'listener'}
            disabled={!supported}
            label="This device's microphone (stand where you listen) · recommended"
            description={supported ? LISTENER_DESCRIPTION : UNSUPPORTED}
            onChange={() => setMode('listener')}
          />
          <Choice
            id={`${baseId}-mics`}
            type="radio"
            name={`${baseId}-mode`}
            checked={mode === 'mics'}
            label="Speaker microphones"
            description={CALIBRATE_DESCRIPTION}
            onChange={() => setMode('mics')}
          />
        </fieldset>
        {phase && <p role="status" className="text-sm font-medium">{PHASES[phase]}</p>}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <Button aria-disabled={pending || undefined} onClick={() => void start()}>
            {pending && <Loader2Icon aria-hidden="true" className="animate-spin" />}
            {mode === 'listener'
              ? 'Start listening'
              : 'Play the chirps'}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
