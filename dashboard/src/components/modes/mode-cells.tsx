import type { ModeView } from '@/api/gen'

// the words that name the mode by voice, a line per language; an owner's own words, set as text
export function ModeAliases({ mode }: { mode: ModeView }) {
  const lines = (['en', 'ru'] as const)
    .map(lang => ({ lang, words: mode.aliases[lang] ?? [] }))
    .filter(line => line.words.length > 0)
  if (lines.length === 0)
    return <span className="text-muted-foreground">—</span>
  return (
    <ul className="flex flex-col gap-0.5">
      {lines.map(({ lang, words }) => (
        <li key={lang} className="min-w-28 whitespace-normal">
          <span className="mr-1.5 font-mono text-xs text-muted-foreground">{lang}</span>
          <span lang={lang}>{words.join(', ')}</span>
        </li>
      ))}
    </ul>
  )
}

// a model id may break after a provider's slash, never inside a name
export function ModelId({ id }: { id: string }) {
  const parts = id.split('/')
  return (
    <span className="font-mono text-xs whitespace-normal">
      {parts.map((part, i) => (
        // eslint-disable-next-line react/no-array-index-key -- the parts of one id, in order
        <span key={i} className="whitespace-nowrap">
          {i < parts.length - 1
            ? `${part}/`
            : part}
          {i < parts.length - 1 && <wbr />}
        </span>
      ))}
    </span>
  )
}
