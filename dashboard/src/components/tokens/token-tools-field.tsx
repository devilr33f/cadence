import type { Tool as McpTool } from '@/api/gen'
import { FieldMessage } from '@/components/forms/fields/field-shell'
import { describedBy } from '@/components/forms/fields/field-text'

// the MCP tools an mcp token may call: every one (none named) or a choice.
// The list is the server's own (GET /api/v1/mcp/tools); a name the server no
// longer serves is dropped when the choice is saved.
export function TokenToolsField({ id, tools, value, onChange }: {
  id: string
  tools: McpTool[] | undefined
  value: readonly string[]
  onChange: (next: string[]) => void
}) {
  const all = value.length === 0
  return (
    <fieldset className="flex flex-col gap-3" aria-describedby={describedBy(id, {})}>
      <legend className="mb-2 text-sm font-medium">MCP tools</legend>
      <label className="flex items-start gap-2.5">
        <input
          type="checkbox"
          className="mt-0.5 size-4"
          checked={all}
          onChange={e => onChange(e.target.checked
            ? []
            : (tools ?? []).slice(0, 1).map(t => t.name))}
        />
        <span className="flex flex-col gap-0.5">
          <span className="text-sm font-medium">Every tool</span>
          <span className="text-sm text-muted-foreground">The whole toolset the endpoint serves.</span>
        </span>
      </label>
      {!all && (
        <div className="max-h-64 overflow-y-auto rounded-md border p-3 flex flex-col gap-2">
          {(tools ?? []).map(tool => (
            <label key={tool.name} className="flex items-start gap-2.5">
              <input
                type="checkbox"
                className="mt-0.5 size-4"
                checked={value.includes(tool.name)}
                onChange={e => onChange(e.target.checked
                  ? [...value, tool.name]
                  : value.filter(n => n !== tool.name))}
              />
              <span className="flex flex-col gap-0.5">
                <span className="font-mono text-sm">{tool.name}</span>
                <span className="text-sm text-muted-foreground">{tool.description}</span>
              </span>
            </label>
          ))}
          {tools === undefined && <span className="text-sm text-muted-foreground">Loading the tools…</span>}
        </div>
      )}
      <FieldMessage id={id} />
    </fieldset>
  )
}
