// the one line that enrolls a speaker with its code, and where the variables it uses are set: the sim's own cadenced
// inside its image (deploy/dev/e2e.sh), a real board through the installer on the owner's machine, as its readme runs
// it ($speaker and $tarball are that readme's). enroll-command.test.ts holds each line equal to its readme's
export interface EnrollHint {
  // none for a screen that has no shell to run one in: its code is typed on the tv
  command?: string
  readme?: string
  // a screen: it joins device-first (a claim it shows, approved at /claims), and this code is its fallback
  screen?: boolean
}

// the screen boards: each joins by itself, showing a 6-character code and a qr the owner approves at /claims; the
// 8-character code is typed on the tv instead, under «У меня есть код из панели» (design §22)
export const SCREEN_BOARDS: readonly string[] = ['sim-tv', 'rpi', 'android-tv']

// the boards the installer swaps, by their readme
export const INSTALL_READMES: Readonly<Record<string, string>> = {
  'yandex-pickle': 'deploy/pickle/README.md',
  'yandex-midi': 'deploy/midi/README.md',
  'vk-capsula-mini': 'deploy/vk-capsula-mini/README.md',
}

// what a board's line adds after the server: midi refuses to install unless the router blocks its vendor's ota
const INSTALL_FLAGS: Readonly<Record<string, string>> = {
  'yandex-midi': ' --ota-blocked',
}

export function enrollHint(board: string, origin: string, code: string): EnrollHint {
  if (board === 'sim-tv') {
    return {
      command: `cadenced enroll --server ${origin} --code ${code} --board sim-tv --config /var/lib/cadence/config.json --data-dir /var/lib/cadence`,
      screen: true,
    }
  }
  if (SCREEN_BOARDS.includes(board))
    return { screen: true }
  if (board === 'sim') {
    return {
      command: `cadenced enroll --server ${origin} --code ${code} --board sim --config /var/lib/cadence/config.json --data-dir /var/lib/cadence`,
    }
  }
  const flags = Object.hasOwn(INSTALL_FLAGS, board)
    ? INSTALL_FLAGS[board]!
    : ''
  const command = `build/cadence-install.exe $speaker --tarball "$tarball" --server ${origin}${flags} --code ${code}`
  return Object.hasOwn(INSTALL_READMES, board)
    ? { command, readme: INSTALL_READMES[board]! }
    : { command }
}
