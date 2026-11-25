import type { CapabilityView, SecretView } from '@/api/gen'

export interface CapabilitySchemas {
  // undefined: the config is edited as raw json
  config?: unknown
  commands: Readonly<Record<string, unknown>>
}

// the json schemas the device declares, for the form renderer (components/forms); one it declares none for (null) is
// edited as raw json
export function capabilitySchemas(capability: CapabilityView): CapabilitySchemas {
  return {
    config: capability.configSchema ?? undefined,
    commands: Object.fromEntries(capability.commands.map(command => [command.name, command.argsSchema ?? undefined])),
  }
}

// the secrets the device declares, in its order, each with whether it has a value
export function capabilitySecrets(capability: CapabilityView): SecretView[] {
  const set = new Set(capability.secrets.filter(secret => secret.set).map(secret => secret.name))
  return capability.secretSpecs.map(spec => ({
    name: spec.name,
    description: spec.description ?? '',
    set: set.has(spec.name),
  }))
}
