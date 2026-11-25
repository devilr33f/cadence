// event labels under a selected span's bar: events a few milliseconds apart sit on one pixel, so their names go
// into lanes, each label in the first lane where it clears the one before it, and none past the track's edges

export type LabelAlign = 'start' | 'center' | 'end'

export interface LanedLabel {
  // the mark's index in the input
  index: number
  lane: number
  align: LabelAlign
}

export interface LaneLayout {
  labels: LanedLabel[]
  lanes: number
}

// a monospace label's width at the waterfall's 11px label size
export const LABEL_CHAR_PX = 6.7
const GAP_PX = 8

export function labelWidthPx(name: string): number {
  return Math.ceil(name.length * LABEL_CHAR_PX)
}

// at: each mark's place on the track, 0 to 1; widths: each label's width in px
export function layoutLabels(at: number[], widths: number[], trackPx: number): LaneLayout {
  const order = at.map((_, i) => i).sort((a, b) => (at[a] ?? 0) - (at[b] ?? 0) || a - b)
  // the right edge of the last label in each lane
  const ends: number[] = []
  const labels: LanedLabel[] = []
  for (const index of order) {
    const x = Math.min(1, Math.max(0, at[index] ?? 0)) * trackPx
    const w = widths[index] ?? 0
    let align: LabelAlign = 'center'
    let left = x - w / 2
    if (left < 0) {
      align = 'start'
      left = x
    }
    else if (x + w / 2 > trackPx) {
      align = 'end'
      left = x - w
    }
    let lane = ends.findIndex(end => end + GAP_PX <= left)
    if (lane < 0) {
      lane = ends.length
      ends.push(0)
    }
    ends[lane] = left + w
    labels.push({ index, lane, align })
  }
  labels.sort((a, b) => a.index - b.index)
  return { labels, lanes: ends.length }
}
