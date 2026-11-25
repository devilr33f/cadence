import type { FavoriteEntry, HistoryEntry } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { listMusicFavoritesOptions, listPlayHistoryOptions, rateMusicSongMutation } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'

export const HISTORY_PAGE = 50
export const HISTORY_MAX = 100

export function historyQueryOptions(api: Client, deviceId: string, limit: number, before = '') {
  return sharedKey(listPlayHistoryOptions({
    client: api,
    query: {
      ...(deviceId !== '' && { deviceId }),
      limit,
      ...(before !== '' && { before }),
    },
  }))
}

export function favoritesQueryOptions(api: Client, query = '') {
  return sharedKey(listMusicFavoritesOptions({
    client: api,
    query: {
      limit: 200,
      ...(query !== '' && { query }),
    },
  }))
}

// favoriteAsHistory reuses the table's title, artwork, source and rating cells
export function favoriteAsHistory(entry: FavoriteEntry, index: number): HistoryEntry {
  return {
    id: index + 1,
    deviceId: '',
    deviceName: '',
    source: 'music',
    provider: entry.provider,
    kind: 'track',
    title: entry.title,
    artist: entry.artist,
    album: entry.album,
    artworkUrl: entry.artworkUrl,
    providerSongId: entry.providerSongId,
    webUrl: entry.webUrl,
    feedback: entry.feedback as HistoryEntry['feedback'],
    favorite: entry.favorite,
    startedAt: entry.updatedAt,
    playedMs: 0,
  }
}

export function useRateMusicSong(api: Client) {
  const queryClient = useQueryClient()
  return useMutation({
    ...rateMusicSongMutation({ client: api }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: sharedKey(listPlayHistoryOptions({ client: api })).queryKey }),
        queryClient.invalidateQueries({ queryKey: sharedKey(listMusicFavoritesOptions({ client: api })).queryKey }),
      ])
    },
  })
}

const SOURCES: Record<string, string> = {
  music: 'Music',
  video: 'Video',
  torrplayer: 'TorrPlayer',
  torrserver: 'TorrServer',
  url: 'URL',
  airplay: 'AirPlay',
  bluetooth: 'Bluetooth',
  soloist: 'Spotify Connect',
  stream: 'Stream',
  screencast: 'Screen mirroring',
  external: 'Another app',
  group: 'Multiroom',
  unspecified: 'Unknown',
}

const PROVIDERS: Record<string, string> = {
  ytmusic: 'YouTube Music',
  youtube: 'YouTube',
  spotify: 'Spotify',
  yandex: 'Yandex Music',
  zvuk: 'Zvuk',
  soundcloud: 'SoundCloud',
  vk: 'VK Music',
}

export function historySource(entry: Pick<HistoryEntry, 'source' | 'provider'>): string {
  const source = SOURCES[entry.source] ?? entry.source
  const provider = PROVIDERS[entry.provider ?? ''] ?? entry.provider
  return provider && provider.toLowerCase() !== source.toLowerCase()
    ? `${source} · ${provider}`
    : source
}
