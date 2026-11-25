import { Link } from '@tanstack/react-router'
import { buttonVariants } from '@/components/ui/button-variants'

// a source is a provider instance, put in the catalog by the global catalogSources setting: both are set elsewhere
export function CatalogLinks() {
  return (
    <div className="flex flex-wrap gap-2">
      <Link to="/providers" className={buttonVariants({ variant: 'outline' })}>Manage the providers</Link>
      <Link to="/settings" className={buttonVariants({ variant: 'outline' })}>Choose the catalog sources</Link>
    </div>
  )
}
