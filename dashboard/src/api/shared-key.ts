// the generated keys carry the client's baseUrl, absolute on the ssr server and relative in the browser; without it the
// queries the server fetched hydrate under the keys the browser asks for, and the internal origin stays out of the page
export function sharedKey<T extends { queryKey: readonly [object] }>(options: T): T {
  const { baseUrl: _baseUrl, ...key } = options.queryKey[0] as { baseUrl?: unknown }
  return {
    ...options,
    queryKey: [key] as unknown as T['queryKey'],
  }
}
