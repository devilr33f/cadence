import type { SettingKey } from './settings-catalog'
import type { SettingsReply } from '@/api/gen'
import type { SettingsScope } from '@/lib/settings'
import { useQuery } from '@tanstack/react-query'
import { useApi } from '@/api/use-api'
import { ErrorState, LoadingLines } from '@/components/data/states'
import { Table, TableBody, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { scopeQueryOptions } from '@/lib/settings'
import { settingsQueryOptions } from '@/lib/time-zone'
import { SettingRow } from './setting-row'
import { settingGroups } from './settings-catalog'

// both scopes are sparse: a key the device sets overrides the global one, which overrides the server's default
export function SettingsEditor({ scope, keys, groupTitle }: { scope: SettingsScope, keys?: readonly SettingKey[], groupTitle?: string }) {
  const api = useApi()
  const own = useQuery(scopeQueryOptions(api, scope))
  const global = useQuery(settingsQueryOptions(api))

  if (own.isPending || global.isPending)
    return <LoadingLines label="Loading the settings" />
  if (own.isError || global.isError) {
    return (
      <div className="rounded-lg border bg-card">
        <ErrorState
          title="The settings could not be read"
          error={own.error ?? global.error}
          onRetry={() => {
            void own.refetch()
            void global.refetch()
          }}
        />
      </div>
    )
  }

  // the global scope inherits nothing: what it leaves unset is the server's default
  const inherited: SettingsReply = scope.kind === 'device'
    ? global.data
    : {}
  const value = (settings: SettingsReply, key: keyof SettingsReply): unknown => settings[key]
  const groups = settingGroups(scope.kind)
    .map(group => ({
      ...group,
      settings: keys
        ? group.settings.filter(spec => keys.includes(spec.key))
        : group.settings,
    }))
    .filter(group => group.settings.length > 0)
  return (
    <div className="flex flex-col gap-6">
      {groups.map(group => (
        // one column rhythm across the groups; a phone scrolls the table inside its box
        <Table key={group.title} className="min-w-[36rem] table-fixed">
          <caption className="px-3 pt-3 pb-1 text-left text-sm font-semibold">{groupTitle ?? group.title}</caption>
          <colgroup>
            <col className="w-[34%]" />
            <col />
            <col className="w-32" />
            <col className="w-44" />
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Setting</TableHead>
              <TableHead>In force</TableHead>
              <TableHead>From</TableHead>
              <TableHead><span className="sr-only">Actions</span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {group.settings.map(spec => (
              <SettingRow
                key={spec.key}
                scope={scope}
                spec={spec}
                own={value(own.data, spec.key)}
                inherited={value(inherited, spec.key)}
              />
            ))}
          </TableBody>
        </Table>
      ))}
    </div>
  )
}
