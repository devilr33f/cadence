import type { WaterfallMark } from '@/lib/traces/waterfall'
import { useLayoutEffect, useRef, useState } from 'react'
import { spanDurationText } from '@/lib/traces/format'
import { labelWidthPx, layoutLabels } from '@/lib/traces/mark-lanes'

// one lane's height: the 11px label and its gap
const LANE_PX = 14
// the ticks' row above the lanes, at each event's time
const TICKS_PX = 6
// before the first measure (and in tests, where nothing lays out), a track as wide as the narrowest waterfall's
const FALLBACK_TRACK_PX = 400

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [width, setWidth] = useState(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el)
      return
    setWidth(el.getBoundingClientRect().width)
    if (typeof ResizeObserver === 'undefined')
      return
    const observer = new ResizeObserver(([entry]) => entry && setWidth(entry.contentRect.width))
    observer.observe(el)
    return () => observer.disconnect()
  }, [])
  return [ref, width] as const
}

const alignClass = {
  start: '',
  center: '-translate-x-1/2',
  end: '-translate-x-full',
} as const

// the selected span's event names, each under a tick at its event's time, in as many lanes as keep them apart
export function MarkLabels({ marks }: { marks: WaterfallMark[] }) {
  const [ref, measured] = useWidth<HTMLSpanElement>()
  const trackPx = measured > 0
    ? measured
    : FALLBACK_TRACK_PX
  const layout = layoutLabels(marks.map(m => m.at), marks.map(m => labelWidthPx(m.name)), trackPx)
  return (
    <span ref={ref} aria-hidden="true" className="relative block" style={{ height: TICKS_PX + layout.lanes * LANE_PX }}>
      {layout.labels.map(({ index }) => marks[index] && (
        <span
          key={`tick-${index}`}
          className="absolute top-0 h-1.5 w-px -translate-x-1/2 bg-foreground"
          style={{ left: `${(marks[index].at * 100).toFixed(3)}%` }}
        />
      ))}
      {layout.labels.map(({ index, lane, align }) => {
        const mark = marks[index]
        if (!mark)
          return null
        return (
          <span
            key={`${mark.name}-${mark.offsetMs}-${index}`}
            title={`${mark.name} +${spanDurationText(mark.offsetMs)}`}
            className={`absolute font-mono text-[0.6875rem] leading-none whitespace-nowrap text-foreground ${alignClass[align]}`}
            style={{ left: `${(mark.at * 100).toFixed(3)}%`, top: TICKS_PX + lane * LANE_PX + 2 }}
          >
            {mark.name}
          </span>
        )
      })}
    </span>
  )
}
