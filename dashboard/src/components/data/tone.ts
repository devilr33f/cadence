import type { Glyph } from './status-glyph'

export type Tone = 'success' | 'warning' | 'danger' | 'info' | 'neutral'

export const TONE_TEXT: Record<Tone, string> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-destructive',
  info: 'text-info',
  neutral: 'text-muted-foreground',
}

export const TONE_SURFACE: Record<Tone, string> = {
  success: 'bg-success-surface',
  warning: 'bg-warning-surface',
  danger: 'bg-destructive-surface',
  info: 'bg-info-surface',
  neutral: 'bg-muted',
}

export const TONE_GLYPH: Record<Tone, Glyph> = {
  success: 'dot',
  warning: 'triangle',
  danger: 'square',
  info: 'half',
  neutral: 'ring',
}
