import type { RefObject } from 'react'
import type { PutSkillRequest, SkillView } from '@/api/gen'
import { useState } from 'react'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { SkillForm } from './skill-form'

// what the dialog is open on: a new skill, or one to edit (a built-in's edit makes the owner's copy)
export type SkillTarget = { kind: 'create' } | { kind: 'edit', skill: SkillView }

// the form goes the moment the dialog starts closing (keyed by the opening), so a reopen starts from what it opens on;
// it cannot be dismissed while the put is on its way
export function SkillDialog({ target, onClose, finalFocus, taken, onSave }: {
  target?: SkillTarget
  onClose: () => void
  finalFocus: RefObject<HTMLElement | null>
  taken: readonly string[]
  onSave: (name: string, body: PutSkillRequest) => Promise<void>
}) {
  const [pending, setPending] = useState(false)
  const [opening, setOpening] = useState(0)
  const [shown, setShown] = useState(target)
  if (target && target !== shown)
    setShown(target)
  const close = () => {
    setOpening(n => n + 1)
    onClose()
  }
  const skill = shown?.kind === 'edit'
    ? shown.skill
    : undefined
  const builtin = skill?.source === 'builtin'
  return (
    <Dialog open={target !== undefined} onOpenChange={next => !next && !pending && close()}>
      <DialogContent finalFocus={finalFocus} className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {skill
              ? `${builtin ? 'Customize' : 'Edit'} ${skill.name}`
              : 'Add a skill'}
          </DialogTitle>
          <DialogDescription>
            {builtin
              ? 'Saving stores your copy, which replaces the built-in skill until you delete it.'
              : 'Instructions a turn loads only when what was said calls for them, so every other turn stays short.'}
          </DialogDescription>
        </DialogHeader>
        <SkillForm
          key={`${opening}-${skill?.name ?? ''}`}
          skill={skill}
          taken={taken}
          onSave={onSave}
          onDone={close}
          onPendingChange={setPending}
        />
      </DialogContent>
    </Dialog>
  )
}
