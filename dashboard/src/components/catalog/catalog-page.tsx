import type { ProviderView } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { LibraryIcon } from 'lucide-react'
import { useId } from 'react'
import { useApi } from '@/api/use-api'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState, LoadingLines } from '@/components/data/states'
import { StatusBadge } from '@/components/data/status-badge'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { catalogSourcesQueryOptions, moduleName } from '@/lib/catalog'
import { CatalogLinks } from './catalog-links'
import { CatalogSearch } from './catalog-search'

// one provider instance of the catalog: its module, what it can do, its sources (torrplayer's own, each on or off) and
// a failure as its code
function ProviderCard({ provider }: { provider: ProviderView }) {
  const titleId = useId()
  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 id={titleId} className="font-mono text-sm font-medium">{provider.instance}</h3>
        <span className="text-sm text-muted-foreground">{moduleName(provider.id)}</span>
        {provider.failure
          ? <StatusBadge tone="danger" label="Failing" detail={provider.failure} />
          : <StatusBadge tone="success" label="Answering" />}
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {provider.capabilities.length > 0 && (
          <p>
            <span className="text-muted-foreground">Can </span>
            {provider.capabilities.join(', ')}
          </p>
        )}
        {provider.sources.length > 0 && (
          <ul aria-label={`Sources of ${provider.instance}`} className="flex flex-col gap-1">
            {provider.sources.map(s => (
              <li key={`${s.provider}:${s.source ?? ''}`} className="flex flex-wrap items-center gap-x-2">
                <span>{s.name}</span>
                {s.enabled
                  ? <StatusBadge tone="success" label="On" />
                  : <StatusBadge tone="neutral" label="Off" />}
                {s.torrents && <span className="text-xs text-muted-foreground">torrents</span>}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

// the catalog the TV browses and the agent searches: the providers the catalogSources setting names, in its order,
// and a search to test them. a provider is made and edited on the providers page, chosen in settings
export function CatalogPage() {
  const api = useApi()
  const list = useQuery(catalogSourcesQueryOptions(api))
  const providers = list.data?.providers ?? []
  return (
    <div className="flex flex-col gap-8">
      <div>
        <PageHeader
          title="Catalog"
          description="Where the TV and the assistant find films, series, anime and music: the provider instances the catalogSources setting names, in its order. Make, test and edit one on the providers page."
          actions={<CatalogLinks />}
        />
        {list.isPending && <LoadingLines lines={3} label="Loading the catalog's sources" />}
        {list.isError && <ErrorState title="The catalog's sources could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        {list.isSuccess && providers.length === 0 && (
          <div className="rounded-lg border bg-card">
            <EmptyState
              icon={LibraryIcon}
              title="No catalog sources yet"
              description="Make a TorrServer or torrplayer provider, then name it in the catalogSources setting."
              action={<CatalogLinks />}
            />
          </div>
        )}
        {providers.length > 0 && (
          <div className="flex flex-col gap-3">
            {providers.map(p => <ProviderCard key={p.instance} provider={p} />)}
          </div>
        )}
      </div>
      {providers.length > 0 && <CatalogSearch />}
    </div>
  )
}
