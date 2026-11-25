import type { QueryClient } from '@tanstack/react-query'
import type { PutSkillRequest } from '@/api/gen'
import type { Client } from '@/api/gen/client'
import { useQueryClient } from '@tanstack/react-query'
import { deleteSkill, putSkill } from '@/api/gen'
import { listSkillsOptions } from '@/api/gen/@tanstack/react-query.gen'
import { sharedKey } from '@/api/shared-key'
import { useApi } from '@/api/use-api'

export function skillsQueryOptions(api: Client) {
  return sharedKey(listSkillsOptions({ client: api }))
}

// in the cache before the page renders; a failure is the page's to show (and retry), not the navigation's to wait for
export async function prefetchSkills(queryClient: QueryClient, api: Client) {
  await queryClient.prefetchQuery({
    ...skillsQueryOptions(api),
    retry: false,
  })
}

// no live topic covers the skills, so each change refreshes the list itself: a put after any answer, since one whose
// answer was lost may still have landed
export function useSkillActions() {
  const api = useApi()
  const queryClient = useQueryClient()
  const refresh = () => queryClient.invalidateQueries({ queryKey: skillsQueryOptions(api).queryKey })
  return {
    // creates or replaces the owner's skill; under a built-in's name it replaces the built-in
    async put(name: string, body: PutSkillRequest) {
      try {
        await putSkill({
          client: api,
          path: { name },
          body,
          throwOnError: true,
        })
      }
      finally {
        void refresh()
      }
    },
    // the owner's skill goes; a built-in it replaced is back
    async remove(name: string) {
      await deleteSkill({
        client: api,
        path: { name },
        throwOnError: true,
      })
      void refresh()
    },
  }
}
