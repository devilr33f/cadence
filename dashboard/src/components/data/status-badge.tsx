import type { ReactNode } from 'react'
import type { Glyph } from './status-glyph'
import type { Tone } from './tone'
import { cn } from 'cn'
import { StatusGlyph } from './status-glyph'
import { TONE_GLYPH, TONE_SURFACE, TONE_TEXT } from './tone'

export interface StatusBadgeProps {
  tone: Tone
  label: string
  glyph?: Glyph
  detail?: ReactNode
  // inline sits in a table cell; pill stands alone in a header or beside a form
  appearance?: 'inline' | 'pill'
  className?: string
}

// the usual states (success, neutral) keep their words in ink and colour only the glyph; the rest are worded in colour
const LABEL_TEXT: Record<Tone, string> = {
  ...TONE_TEXT,
  success: 'text-foreground',
  neutral: 'text-muted-foreground',
}

export function StatusBadge({ tone, label, glyph, detail, appearance = 'inline', className }: StatusBadgeProps) {
  const quiet = tone === 'success' || tone === 'neutral'
  return (
    <span
      data-tone={tone}
      className={cn(
        'inline-flex items-center gap-1.5 text-sm font-medium whitespace-nowrap',
        LABEL_TEXT[tone],
        appearance === 'pill' && ['h-6 rounded-full px-2.5', TONE_SURFACE[tone]],
        className,
      )}
    >
      <StatusGlyph shape={glyph ?? TONE_GLYPH[tone]} className={TONE_TEXT[tone]} />
      <span>{label}</span>
      {detail !== undefined && <span className={cn('font-normal tabular-nums', quiet && 'text-muted-foreground')}>{detail}</span>}
    </span>
  )
}
