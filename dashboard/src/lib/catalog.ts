import type { QueryClient } from '@tanstack/react-query'
import type { Client } from '@/api/gen/client'
import { listCatalogSourcesOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

// the catalog's providers, as the global catalogSources setting names them, in its order, each with its sources. a
// provider is a provider instance (the providers page makes, tests and deletes it); this list only reads them
export function catalogSourcesQueryOptions(api: Client) {
  return sharedKey(listCatalogSourcesOptions({ client: api }))
}

export async function prefetchCatalog(queryClient: QueryClient, api: Client) {
  await queryClient.prefetchQuery({
    ...catalogSourcesQueryOptions(api),
    retry: false,
  })
}

// a provider module's name as the owner reads it
const MODULE_NAMES: Readonly<Record<string, string>> = {
  'torrserver': 'TorrServer',
  'torrplayer': 'torrplayer',
  'youtube-music': 'YouTube Music',
  'yandex-music': 'Yandex Music',
  'zvuk': 'Zvuk',
  'soundcloud': 'SoundCloud',
  'vk-music': 'VK Music',
}

export function moduleName(id: string): string {
  return MODULE_NAMES[id] ?? id
}
