import type { QueryClient } from '@tanstack/react-query'
import type { WakeWordView } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import {
  deleteWakeWord,
  deleteWakeWordDataset,
  fetchWakeWordNegatives,
  retrainWakeWord,
  trainWakeWord,
  uploadWakeWordModel,
} from '@/api/gen'
import { getWakeWordNegativesOptions, listWakeWordsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export function wakeWordsQueryOptions(api: Client) {
  return sharedKey(listWakeWordsOptions({ client: api }))
}

export function negativesQueryOptions(api: Client) {
  return sharedKey(getWakeWordNegativesOptions({ client: api }))
}

export async function prefetchWakeWords(queryClient: QueryClient, api: Client) {
  await Promise.all([
    queryClient.prefetchQuery({
      ...wakeWordsQueryOptions(api),
      retry: false,
    }),
    queryClient.prefetchQuery({
      ...negativesQueryOptions(api),
      retry: false,
    }),
  ])
}

// the words a device can run: trained or uploaded, with their model
export function readyWords(words: readonly WakeWordView[] | undefined): WakeWordView[] {
  return (words ?? []).filter(w => w.status === 'ready' && w.sha256)
}

// what a dataset refusal's code means, as the owner reads it (pkg/kws's codes)
const DATASET_CODES: Readonly<Record<string, string>> = {
  too_large: 'The zip is larger than 256 MiB.',
  not_zip: 'The file is not a zip.',
  too_many_files: 'The zip holds more than 256 files.',
  file_too_large: 'A file in the zip is larger than 64 MiB.',
  bad_wav: 'A recording is not a WAV file this server can read (16, 24 or 32-bit PCM, or 32-bit float).',
  no_takes: 'The zip has no takes: put each one in a folder named takes.',
  too_many_takes: 'The zip has more than 100 takes.',
  no_ambient: 'The zip has no room recording of a second or more: name it ambient.wav.',
}

// why a training failed (the word's code)
const TRAIN_CODES: Readonly<Record<string, string>> = {
  too_few_takes: 'Fewer than 3 takes had a clear word in them.',
  no_ambient: 'The room recording is missing or too short.',
  no_negatives: 'The negative features are missing: fetch them first.',
  interrupted: 'The server restarted while it trained.',
  internal: 'The server failed to train it.',
  cancelled: 'The training was cancelled.',
}

export function datasetCodeText(code: string | undefined): string | undefined {
  return code ? DATASET_CODES[code] : undefined
}

export function trainCodeText(code: string | undefined): string {
  if (!code)
    return ''
  return TRAIN_CODES[code] ?? code
}

// every change refreshes the list itself too: the live feed's wakewords topic covers the rest
export function useWakeWordActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  const refresh = () => Promise.all([
    queryClient.invalidateQueries({ queryKey: wakeWordsQueryOptions(api).queryKey }),
    queryClient.invalidateQueries({ queryKey: negativesQueryOptions(api).queryKey }),
  ])
  return {
    async uploadModel(file: File, word: string) {
      await uploadWakeWordModel({
        client: api,
        body: file,
        query: { word: word || undefined },
        throwOnError: true,
      })
      void refresh()
    },
    async train(file: File, word: string, keep: boolean) {
      await trainWakeWord({
        client: api,
        body: file,
        query: { word, keep },
        throwOnError: true,
      })
      void refresh()
    },
    async retrain(id: string) {
      await retrainWakeWord({
        client: api,
        path: { id },
        throwOnError: true,
      })
      void refresh()
    },
    async remove(id: string) {
      await deleteWakeWord({
        client: api,
        path: { id },
        throwOnError: true,
      })
      void refresh()
    },
    async dropDataset(id: string) {
      await deleteWakeWordDataset({
        client: api,
        path: { id },
        throwOnError: true,
      })
      void refresh()
    },
    async fetchNegatives() {
      await fetchWakeWordNegatives({ client: api, throwOnError: true })
      void refresh()
    },
  }
}
