// the secrets whose value is a file, which a one-line field cannot take as pasted (it drops the line breaks): the
// server's yt-dlp, x-web and yandex-music modules read their cookies as a Netscape cookies.txt
export const FILE_SECRETS: ReadonlySet<string> = new Set(['cookies'])

// the server's own bound (server/internal/providers/netscape/netscape.go)
export const MAX_FILE_BYTES = 512 * 1024

// an input's value sanitization removes newlines, so a loaded text file lives beside its input instead. weak keys let
// an unmounted field and its secret go together; the form takes and deletes the value on every submit
const fileTexts = new WeakMap<HTMLInputElement, string>()

export function setFileText(input: HTMLInputElement, text: string) {
  input.value = ''
  fileTexts.set(input, text)
}

export function fileTextOf(input: HTMLInputElement): string | undefined {
  return fileTexts.get(input)
}

export function clearFileText(input: HTMLInputElement) {
  fileTexts.delete(input)
}

export function secretInputValue(input: HTMLInputElement): string {
  return fileTexts.get(input) ?? input.value
}
