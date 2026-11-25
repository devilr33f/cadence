import { createFileRoute } from '@tanstack/react-router'
import { CatalogPage } from '@/components/catalog/catalog-page'
import { prefetchCatalog } from '@/lib/catalog'

export const Route = createFileRoute('/_authed/catalog/')({
  loader: ({ context }) => prefetchCatalog(context.queryClient, context.api),
  head: () => ({
    meta: [
      { title: 'Catalog · Cadence' },
    ],
  }),
  component: CatalogPage,
})
