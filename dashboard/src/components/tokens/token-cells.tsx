import type { TokenView } from '@/api/gen'
import { Tag } from '@/components/data/tag'

export function ScopeList({ scopes, mcpTools }: { scopes: TokenView['scopes'], mcpTools?: Array<string> | null }) {
  const named = mcpTools ?? []
  return (
    <ul aria-label="Scopes" className="flex flex-wrap gap-1">
      {scopes.map(scope => <li key={scope}><Tag mono>{scope}</Tag></li>)}
      {scopes.includes('mcp') && (
        <li>
          <Tag mono>
            {named.length === 0
              ? 'mcp: all tools'
              : `mcp: ${named.length} tools`}
          </Tag>
        </li>
      )}
    </ul>
  )
}
