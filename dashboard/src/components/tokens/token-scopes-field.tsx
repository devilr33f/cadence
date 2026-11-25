import type { TokenScope } from '@/lib/tokens'
import { FieldMessage, RequiredMark } from '@/components/forms/fields/field-shell'
import { describedBy } from '@/components/forms/fields/field-text'
import { TOKEN_SCOPES } from '@/lib/tokens'

// what each scope may call (deploy/server/README.md, API tokens and scopes)
const SCOPE_TEXT: Record<TokenScope, string> = {
  admin: 'Everything the dashboard does, tokens included.',
  ha: 'The devices and their media: read them, control playback and volume, announce. What Home Assistant needs.',
  metrics: 'The Prometheus metrics, and nothing else.',
  presence: 'Busy signals: say the owner is in a call or a meeting, or clear it, and nothing else.',
  mcp: 'The MCP endpoint only, with the tools this token may call (every tool unless the list names some).',
}

export function TokenScopesField({ id, value, error, onChange }: {
  id: string
  value: readonly TokenScope[]
  error?: string
  onChange: (next: TokenScope[]) => void
}) {
  return (
    <fieldset
      className="flex flex-col gap-3"
      data-invalid={error ? true : undefined}
      aria-describedby={describedBy(id, { error })}
    >
      <legend className="mb-2 text-sm font-medium">
        Scopes
        <RequiredMark required />
      </legend>
      {TOKEN_SCOPES.map((scope) => {
        const scopeId = `${id}-${scope}`
        return (
          <div key={scope} className="flex items-start gap-2.5">
            <input
              id={scopeId}
              type="checkbox"
              className="mt-0.5 size-4"
              checked={value.includes(scope)}
              aria-invalid={error ? true : undefined}
              aria-describedby={`${scopeId}-description`}
              onChange={e => onChange(e.target.checked
                ? TOKEN_SCOPES.filter(s => s === scope || value.includes(s))
                : value.filter(s => s !== scope))}
            />
            <div className="flex flex-col gap-0.5">
              <label htmlFor={scopeId} className="font-mono text-sm">{scope}</label>
              <p id={`${scopeId}-description`} className="text-sm text-muted-foreground">{SCOPE_TEXT[scope]}</p>
            </div>
          </div>
        )
      })}
      <FieldMessage id={id} error={error} />
    </fieldset>
  )
}
