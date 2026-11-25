import type { Instance, ModuleInfo } from '@/api/gen'
import { kindRank, PROVIDER_KINDS } from './provider-modules'

export interface ProviderRow {
  instance: Instance
  // undefined for a module the server's catalog does not list (no longer registered, or a dev fake on a server without
  // them)
  module?: ModuleInfo
}

export function rowRank(row: ProviderRow): number {
  return row.module
    ? kindRank(row.module.kind)
    : PROVIDER_KINDS.length + 1
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

// by kind, in the page's order, then by name; an unknown module last
export function sortProviders(rows: readonly ProviderRow[]): ProviderRow[] {
  return [...rows].sort((a, b) => rowRank(a) - rowRank(b) || collator.compare(a.instance.name, b.instance.name))
}
