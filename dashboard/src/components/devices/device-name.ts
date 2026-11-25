// the server's rule for a device's name (server/internal/devicename), which it checks again: trimmed, then 1–64
// characters with no control or format character and no line or paragraph separator
export const MAX_NAME = 64

const HIDDEN = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u

// why a trimmed name would be refused, or nothing
export function nameError(name: string): string | undefined {
  if (name === '')
    return 'Give the speaker a name.'
  if ([...name].length > MAX_NAME)
    return `At most ${MAX_NAME} characters.`
  if (HIDDEN.test(name))
    return 'No control or format characters.'
  return undefined
}

// the same rule for a device's room, which may be empty: no room
export function roomError(room: string): string | undefined {
  return room === ''
    ? undefined
    : nameError(room)
}
