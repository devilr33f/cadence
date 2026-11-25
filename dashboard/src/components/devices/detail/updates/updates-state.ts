// pure helpers of the updates tab, tested apart from the requests

export type ArtifactKind = 'tar' | 'apk' | 'deb'

// the artifact kind a board installs with: the yandex speakers a tarball, the tv an apk, the pi a deb
export function kindOfBoard(board: string): ArtifactKind {
  if (board === 'android-tv')
    return 'apk'
  if (board === 'rpi')
    return 'deb'
  return 'tar'
}

// only an apk or deb names its own version; a tarball's comes from its manifest
export function kindNeedsVersion(kind: ArtifactKind): boolean {
  return kind === 'apk' || kind === 'deb'
}

// a run the owner may roll back to the artifact before: the newest one that succeeded with an
// artifact of its own
export function rollbackRun<T extends { status: string, artifactId?: string }>(runs: readonly T[]): T | null {
  return runs.find(run => run.status === 'succeeded' && run.artifactId) ?? null
}

export function updateAvailable(daemonVersion: string | undefined, newestVersion: string | undefined) {
  return Boolean(newestVersion) && daemonVersion !== newestVersion
}

export function runText(status: string): string {
  switch (status) {
    case 'running': return 'running'
    case 'awaiting_restart': return 'awaiting restart'
    case 'succeeded': return 'succeeded'
    case 'failed': return 'failed'
    case 'rolled_back': return 'rolled back'
    case 'needs_attention': return 'needs attention'
    default: return status
  }
}
