import { Link } from '@tanstack/react-router'
import { buttonVariants } from '@/components/ui/button-variants'

// a global setting's row on the settings page, where the integration is named and switched
export function SettingLink({ settingKey, appearance = 'link' }: { settingKey: string, appearance?: 'link' | 'button' }) {
  return (
    <Link
      to="/settings"
      hash={`setting-${settingKey}`}
      className={appearance === 'button'
        ? buttonVariants({ variant: 'outline' })
        : buttonVariants({ variant: 'link' })}
    >
      Open the
      {' '}
      <code className="font-mono text-xs">{settingKey}</code>
      {' '}
      setting
    </Link>
  )
}

export function ProvidersLink() {
  return <Link to="/providers" className={buttonVariants({ variant: 'outline' })}>Add a provider</Link>
}
