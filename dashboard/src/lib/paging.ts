// a list the server reads by a limit alone (no offset), asked for a longer page each time: more while the page came back
// full and under the server's most, and still while a longer one loads (its rows kept as placeholders), so the button
// that asked keeps the focus
export function canAskMore(query: { data?: readonly unknown[], isPlaceholderData: boolean }, limit: number, max: number): boolean {
  if (query.data === undefined)
    return false
  return query.isPlaceholderData || (query.data.length >= limit && limit < max)
}
