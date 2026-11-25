import { Link } from '@tanstack/react-router'
import { PlusIcon } from 'lucide-react'
import { buttonVariants } from '@/components/ui/button-variants'

export function EnrollLink({ size = 'default' }: { size?: 'default' | 'lg' }) {
  return (
    <Link to="/devices/enroll" className={buttonVariants({ size })}>
      <PlusIcon aria-hidden="true" />
      Enroll a speaker
    </Link>
  )
}
