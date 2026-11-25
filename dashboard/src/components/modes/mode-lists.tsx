import type { ModeErrors } from './mode-draft'
import { describedBy } from '@/components/forms/fields/field-text'
import { TagListField } from '@/components/forms/tag-list-field'
import { Switch } from '@/components/ui/switch'
import { aliasError, toolError } from './mode-draft'

// the switch offers every built-in tool (null); off, the list below names the ones the mode offers, none when empty.
// the list is kept while the switch is on, so turning it back off finds it again
export function ModeToolsField({ baseId, allTools, tools, typed, error, onAllTools, onTools, onType }: {
  baseId: string
  allTools: boolean
  tools: readonly string[]
  typed: string
  error?: string
  onAllTools: (all: boolean) => void
  onTools: (tools: string[]) => void
  onType: (text: string) => void
}) {
  const id = `${baseId}-all-tools`
  return (
    <fieldset className="flex flex-col gap-4">
      <legend className="mb-1 text-sm font-medium">Tools</legend>
      <div className="flex items-start gap-3">
        <Switch
          className="mt-0.5"
          checked={allTools}
          aria-labelledby={`${id}-label`}
          aria-describedby={describedBy(id, { description: true })}
          onCheckedChange={onAllTools}
        />
        <div className="flex flex-col gap-0.5">
          <span id={`${id}-label`} className="text-sm font-medium">All tools</span>
          <span id={`${id}-description`} className="text-sm text-muted-foreground">
            Every built-in tool the device&apos;s tools setting allows. Off, the mode offers only the ones named here, none
            while the list is empty. MCP tools follow the device&apos;s list alone.
          </span>
        </div>
      </div>
      {!allTools && (
        <TagListField
          id={`${baseId}-tools`}
          label="Tools"
          mono
          items={tools}
          onChange={onTools}
          draft={typed}
          onDraftChange={onType}
          check={toolError}
          error={error}
        />
      )}
    </fieldset>
  )
}

const LANGUAGES = [
  { lang: 'en', label: 'English aliases' },
  { lang: 'ru', label: 'Russian aliases' },
] as const

// the words that switch to the mode by voice, per language; distinct in any case from each other and from every other
// mode's names and aliases, which the server checks (alias_taken)
export function ModeAliasesFields({ baseId, en, ru, typed, errors, onChange, onType }: {
  baseId: string
  en: readonly string[]
  ru: readonly string[]
  typed: Record<'en' | 'ru', string>
  errors: ModeErrors
  onChange: (lang: 'en' | 'ru', words: string[]) => void
  onType: (lang: 'en' | 'ru', text: string) => void
}) {
  const words = { en, ru }
  return (
    <fieldset className="flex flex-col gap-4">
      <legend className="mb-1 text-sm font-medium">Voice aliases</legend>
      <p className="-mt-2 text-sm text-muted-foreground">Words that switch to this mode when said, up to 10 a language.</p>
      {LANGUAGES.map(({ lang, label }) => (
        <TagListField
          key={lang}
          id={`${baseId}-${lang}`}
          label={label}
          items={words[lang]}
          onChange={next => onChange(lang, next)}
          draft={typed[lang]}
          onDraftChange={text => onType(lang, text)}
          check={aliasError}
          error={errors[lang]}
        />
      ))}
    </fieldset>
  )
}
