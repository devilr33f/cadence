import type { Client } from './gen/client'
import { useRouteContext } from '@tanstack/react-router'

export function useApi(): Client {
  return useRouteContext({
    from: '__root__',
    select: context => context.api,
  })
}
