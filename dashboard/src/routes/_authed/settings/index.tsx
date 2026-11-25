import { createFileRoute } from '@tanstack/react-router'
import { GlobalSettingsPage } from '@/components/settings/global-settings-page'

// the global settings are the signed-in layout's to load (the household's zone is one of them)
export const Route = createFileRoute('/_authed/settings/')({
  head: () => ({
    meta: [
      { title: 'Settings · Cadence' },
    ],
  }),
  component: GlobalSettingsPage,
})
