import { durationText } from '@/lib/format-time'
import { useDraft } from './use-draft'

const TRACK = 'h-1 w-full'

// where the track is, ticking; with `onSeek` a slider the pointer drags or clicks and the keyboard steps (arrows by a
// second, Page keys by a tenth, Home and End), sent once it rests
export function SeekBar({ positionMs, durationMs, onSeek }: {
  positionMs: number
  durationMs: number
  onSeek?: (positionMs: number) => Promise<void>
}) {
  const seek = useDraft(async (seconds) => {
    await onSeek?.(seconds * 1000)
  })
  const at = seek.draft === undefined
    ? positionMs
    : seek.draft * 1000
  const text = `${durationText(at)} of ${durationText(durationMs)}`
  const max = Math.round(durationMs / 1000)
  return (
    <div className="flex flex-col gap-1">
      {onSeek
        ? (
            <input
              type="range"
              aria-label="Position"
              min={0}
              max={max}
              step={1}
              value={Math.min(max, Math.round(at / 1000))}
              aria-valuetext={text}
              onChange={e => seek.change(Number(e.target.value))}
              className={`${TRACK} cursor-pointer accent-foreground`}
            />
          )
        : (
            <div
              role="progressbar"
              aria-label="Position"
              aria-valuemin={0}
              aria-valuemax={max}
              aria-valuenow={Math.round(at / 1000)}
              aria-valuetext={text}
              className={`${TRACK} overflow-hidden rounded-full bg-muted`}
            >
              <div className="h-full rounded-full bg-foreground" style={{ width: `${(at / durationMs) * 100}%` }} />
            </div>
          )}
      <div className="flex justify-between text-xs text-muted-foreground tabular-nums">
        <span>{durationText(at)}</span>
        <span>{durationText(durationMs)}</span>
      </div>
      {seek.error && <p role="alert" className="text-xs text-destructive">{`The seek failed. ${seek.error}`}</p>}
    </div>
  )
}
