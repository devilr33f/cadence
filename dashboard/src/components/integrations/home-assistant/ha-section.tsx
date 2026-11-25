import type { ReactNode } from 'react'
import type { DeviceView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { HouseIcon } from 'lucide-react'
import { useApi } from '@/api/use-api'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { areasQueryOptions, HOME_ASSISTANT_REFRESH_MS, homeAssistantQueryOptions } from '@/lib/home-assistant'
import { ProvidersLink, SettingLink } from '../setting-link'
import { HomeAssistantEntities } from './ha-entities'
import { HomeAssistantStatus } from './ha-status'
import { HomeAssistantAreas, HomeAssistantUsage } from './ha-tables'

function Part({ title, children }: { title: string, children: ReactNode }) {
  return (
    <div className="flex flex-col gap-3">
      <h3 className="text-base font-semibold">{title}</h3>
      {children}
    </div>
  )
}

// what the running client sees; its routes answer 404 not_configured while none runs, so nothing is asked of them then
function RunningClient({ devices }: { devices: readonly DeviceView[] }) {
  const api = useApi()
  const areas = useQuery(areasQueryOptions(api))
  return (
    <>
      <Part title="Areas">
        <HomeAssistantAreas
          areas={areas.data}
          status={areas.isError
            ? 'error'
            : areas.isPending ? 'loading' : 'ready'}
          error={areas.error}
          onRetry={() => void areas.refetch()}
        />
      </Part>
      <Part title="Entities">
        <HomeAssistantEntities areas={areas.data ?? []} />
      </Part>
      <Part title="Most used">
        <HomeAssistantUsage devices={devices} />
      </Part>
    </>
  )
}

export function HomeAssistantSection({ devices }: { devices: readonly DeviceView[] }) {
  const api = useApi()
  const status = useQuery({
    ...homeAssistantQueryOptions(api),
    refetchInterval: HOME_ASSISTANT_REFRESH_MS,
  })
  if (status.isPending)
    return <LoadingLines lines={3} label="Loading the Home Assistant status" />
  if (status.isError) {
    return (
      <div className="rounded-lg border bg-card">
        <ErrorState title="The Home Assistant status could not be read" error={status.error} onRetry={() => void status.refetch()} />
      </div>
    )
  }
  if (status.data.configured === null && status.data.instance === null) {
    return (
      <div className="rounded-lg border bg-card">
        <EmptyState
          icon={HouseIcon}
          title="Home Assistant is not set up"
          description="Add a Home Assistant provider with a long-lived access token, name it in the homeAssistant setting, then restart the server."
          action={(
            <div className="flex flex-wrap justify-center gap-2">
              <ProvidersLink />
              <SettingLink settingKey="homeAssistant" appearance="button" />
            </div>
          )}
        />
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-6">
      <HomeAssistantStatus status={status.data} />
      {status.data.instance !== null && <RunningClient devices={devices} />}
    </div>
  )
}
