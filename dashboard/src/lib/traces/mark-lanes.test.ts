import { describe, expect, it } from 'vitest'
import { labelWidthPx, layoutLabels } from './mark-lanes'

describe('event labels in lanes', () => {
  it('keep labels far apart on one lane', () => {
    const layout = layoutLabels([0.2, 0.5, 0.8], [60, 60, 60], 600)
    expect(layout.lanes).toBe(1)
    expect(layout.labels.map(l => l.lane)).toEqual([0, 0, 0])
  })

  it('stack events a moment apart, so no two labels overlap', () => {
    const names = ['first_audio_sent', 'stream_opened', 'first_result']
    const layout = layoutLabels([0.5, 0.501, 0.502], names.map(labelWidthPx), 600)
    expect(layout.lanes).toBe(3)
    expect(new Set(layout.labels.map(l => l.lane)).size).toBe(3)
  })

  it('reuse a lane once a label has cleared it', () => {
    const layout = layoutLabels([0.1, 0.11, 0.9], [80, 80, 80], 600)
    expect(layout.lanes).toBe(2)
    expect(layout.labels.map(l => l.lane)).toEqual([0, 1, 0])
  })

  it('keep labels at the track ends inside it', () => {
    const layout = layoutLabels([0, 0.5, 1], [100, 100, 100], 600)
    expect(layout.labels.map(l => l.align)).toEqual(['start', 'center', 'end'])
  })

  it('answer in the input order whatever the times', () => {
    const layout = layoutLabels([0.9, 0.1], [50, 50], 600)
    expect(layout.labels.map(l => l.index)).toEqual([0, 1])
  })
})
