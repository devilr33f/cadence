import type { ReactNode } from 'react'
import type { CapabilityView } from '@/api/gen'
import { useId } from 'react'
import { KeyValueList } from '@/components/data/key-value-list'
import { isObject } from '@/components/forms/json-pointer'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { CapabilityBadge } from './capability-badge'
import { CapabilityCommands } from './capability-commands'
import { CapabilityConfig } from './capability-config'
import { capabilitySchemas, capabilitySecrets } from './capability-schemas'
import { jsonItems } from './json-items'
import { SecretRow } from './secret-row'

function Part({ title, children }: { title: string, children: ReactNode }) {
  const titleId = useId()
  return (
    <div role="group" aria-labelledby={titleId} className="flex flex-col gap-2">
      <h4 id={titleId} className="text-sm font-semibold">{title}</h4>
      {children}
    </div>
  )
}

function State({ state }: { state: unknown }) {
  if (state === null || state === undefined)
    return <p className="text-sm text-muted-foreground">No state reported.</p>
  if (!isObject(state))
    return <pre className="font-mono text-xs/5 break-all whitespace-pre-wrap">{JSON.stringify(state)}</pre>
  return <KeyValueList items={jsonItems(state)} />
}

// one capability: what the device reports, what it should run with (and whether it does), its secrets and commands
export function CapabilityCard({ deviceId, capability }: { deviceId: string, capability: CapabilityView }) {
  const titleId = useId()
  const schemas = capabilitySchemas(capability)
  const secrets = capabilitySecrets(capability)
  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {/* under the page's Capabilities heading, over the card's parts */}
        <h3 id={titleId} className="font-mono text-sm font-medium">{capability.id}</h3>
        <span className="text-sm text-muted-foreground">{`v${capability.version}`}</span>
        <CapabilityBadge capability={capability} />
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <Part title="State">
          <State state={capability.state} />
        </Part>
        <Part title="Desired configuration">
          <CapabilityConfig deviceId={deviceId} capability={capability} schema={schemas.config} />
        </Part>
        {secrets.length > 0 && (
          <Part title="Secrets">
            <ul className="flex flex-col">
              {secrets.map(secret => (
                <SecretRow key={secret.name} where={{ deviceId, capability: capability.id }} secret={secret} />
              ))}
            </ul>
          </Part>
        )}
        {capability.commands.length > 0 && (
          <Part title="Commands">
            <CapabilityCommands deviceId={deviceId} capability={capability} schemas={schemas.commands} />
          </Part>
        )}
      </CardContent>
    </Card>
  )
}
