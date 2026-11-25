import type { ProviderRow } from './provider-rows'
import { StatusBadge } from '@/components/data/status-badge'

// a secret answers set or not, never a value: the module's own (in the catalog's order, required ones first), else
// what the instance has
export function ProviderSecrets({ row }: { row: ProviderRow }) {
  const { module, instance } = row
  const secrets = module
    ? module.secrets
    : Object.keys(instance.secrets).map(name => ({ name, required: false }))
  if (secrets.length === 0) {
    return (
      <span className="text-muted-foreground">
        {module ? 'None' : '—'}
      </span>
    )
  }
  return (
    <ul className="flex flex-col gap-0.5">
      {secrets.map(({ name, required }) => {
        const set = instance.secrets[name]?.set === true
        return (
          <li key={name} className="flex items-center gap-2">
            <span className="font-mono text-xs">{name}</span>
            {set
              ? <StatusBadge tone="success" label="Set" />
              : <StatusBadge tone={required ? 'warning' : 'neutral'} label="Not set" />}
          </li>
        )
      })}
    </ul>
  )
}
