import type { UserEvent } from '@testing-library/user-event'
import userEvent from '@testing-library/user-event'

type Options = Parameters<typeof userEvent.setup>[0]

// user-event without its pause between actions: the default (delay 0) yields to the task queue after every key and
// pointer step, which a form re-rendered per keystroke turns into seconds, and a loaded gate into a timeout. waits
// belong in the tests' own findBy and waitFor
export function setupUser(options: Options = {}): UserEvent {
  return userEvent.setup({ delay: null, ...options })
}

// a value entered in one input event, as a paste, for a test about what is sent or refused rather than about typing
export async function enter(user: UserEvent, element: HTMLElement, text: string) {
  await user.click(element)
  await user.paste(text)
}

// the field emptied first, then the value entered as above
export async function replace(user: UserEvent, element: HTMLElement, text: string) {
  await user.clear(element)
  if (text !== '')
    await user.paste(text)
}
