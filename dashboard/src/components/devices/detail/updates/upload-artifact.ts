// the artifact upload is a plain route, not a huma operation, so no generated call exists: this is
// the one hand-written request, with the client's own rules (same-origin cookies, no store, the
// csrf header a non-get cookie request carries)
export async function uploadArtifact(input: {
  board: string
  kind: string
  label: string
  version?: string
  file: File
}): Promise<void> {
  const query = new URLSearchParams({ board: input.board, kind: input.kind, label: input.label })
  if (input.version)
    query.set('version', input.version)
  const response = await fetch(`/api/v1/device-artifacts?${query.toString()}`, {
    method: 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    headers: { 'X-Cadence-CSRF': '1', 'Content-Type': 'application/octet-stream' },
    body: input.file,
  })
  if (!response.ok)
    throw new Error(`upload failed (${response.status})`)
}
