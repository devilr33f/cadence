import { cn } from 'cn'

export type Glyph = 'dot' | 'ring' | 'half' | 'dashed' | 'triangle' | 'square'

// state reads by shape as well as colour: in greyscale, for colour-blind readers and in print
const SHAPES: Record<Glyph, React.ReactNode> = {
  dot: <circle cx="5" cy="5" r="4" fill="currentColor" />,
  ring: <circle cx="5" cy="5" r="3.25" fill="none" stroke="currentColor" strokeWidth="1.5" />,
  half: (
    <>
      <circle cx="5" cy="5" r="3.25" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M5 1a4 4 0 0 0 0 8z" fill="currentColor" />
    </>
  ),
  dashed: <circle cx="5" cy="5" r="3.25" fill="none" stroke="currentColor" strokeWidth="1.5" strokeDasharray="2.4 1.7" />,
  triangle: <path d="M5 .9 9.5 8.9H.5z" fill="currentColor" />,
  square: <rect x="1.25" y="1.25" width="7.5" height="7.5" rx="1" fill="currentColor" />,
}

export function StatusGlyph({ shape, className }: { shape: Glyph, className?: string }) {
  return (
    <svg viewBox="0 0 10 10" aria-hidden="true" focusable="false" className={cn('size-2.5 shrink-0', className)}>
      {SHAPES[shape]}
    </svg>
  )
}
