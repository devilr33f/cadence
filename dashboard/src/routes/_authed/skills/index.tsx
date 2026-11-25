import { createFileRoute } from '@tanstack/react-router'
import { SkillsPage } from '@/components/skills/skills-page'
import { prefetchSkills } from '@/lib/skills'

export const Route = createFileRoute('/_authed/skills/')({
  loader: ({ context }) => prefetchSkills(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Skills · Cadence' },
    ],
  }),
  component: SkillsPage,
})
