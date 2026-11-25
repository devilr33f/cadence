import { PageHeader } from '@/components/data/page-header'
import { GLOBAL_SCOPE } from '@/lib/settings'
import { SettingsEditor } from './settings-editor'

export function GlobalSettingsPage() {
  return (
    <>
      <PageHeader
        title="Settings"
        description="The household's defaults: every speaker takes them unless its own page overrides a key, and a key set nowhere takes the server's default."
      />
      <SettingsEditor scope={GLOBAL_SCOPE} />
    </>
  )
}
