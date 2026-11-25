import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { enrollHint, INSTALL_READMES } from './enroll-command'

const ROOT = join(import.meta.dirname, '../../../../..')

// the lines a readme runs, as written in its sh blocks
function documentedLines(readme: string): string[] {
  return readFileSync(join(ROOT, readme), 'utf8').split('\n').map(line => line.trim())
}

describe('the enroll wizard\'s install hint', () => {
  // the readmes write the server and the code as placeholders
  it.each(Object.entries(INSTALL_READMES))('is the line %s\'s readme documents', (board, readme) => {
    const hint = enrollHint(board, 'https://<v2 host>', '<code>')
    expect(hint.readme).toBe(readme)
    expect(documentedLines(readme)).toContain(hint.command)
  })

  it('names the installer boards\' readmes', () => {
    expect(INSTALL_READMES).toEqual({
      'yandex-pickle': 'deploy/pickle/README.md',
      'yandex-midi': 'deploy/midi/README.md',
      'vk-capsula-mini': 'deploy/vk-capsula-mini/README.md',
    })
  })

  it('fills in this origin and the code', () => {
    expect(enrollHint('yandex-midi', 'https://cadence.example', 'K7Q2-9XPL').command).toBe(
      'build/cadence-install.exe $speaker --tarball "$tarball" --server https://cadence.example --ota-blocked --code K7Q2-9XPL',
    )
  })

  it('gives the sim its own cadenced, as the dev stack runs it, and no readme', () => {
    expect(enrollHint('sim', 'http://127.0.0.1:18080', 'K7Q2-9XPL')).toEqual({
      command: 'cadenced enroll --server http://127.0.0.1:18080 --code K7Q2-9XPL --board sim --config /var/lib/cadence/config.json --data-dir /var/lib/cadence',
    })
  })

  it('gives a board no readme documents yet the installer\'s plain line', () => {
    expect(enrollHint('toaster', 'https://cadence.example', 'K7Q2-9XPL')).toEqual({
      command: 'build/cadence-install.exe $speaker --tarball "$tarball" --server https://cadence.example --code K7Q2-9XPL',
    })
  })

  it('tells a screen board it joins device-first, the code its fallback', () => {
    expect(enrollHint('android-tv', 'https://cadence.example', 'K7Q2-9XPL')).toEqual({ screen: true })
    expect(enrollHint('rpi', 'https://cadence.example', 'K7Q2-9XPL')).toEqual({ screen: true })
    expect(enrollHint('sim-tv', 'http://127.0.0.1:18080', 'K7Q2-9XPL')).toEqual({
      command: 'cadenced enroll --server http://127.0.0.1:18080 --code K7Q2-9XPL --board sim-tv --config /var/lib/cadence/config.json --data-dir /var/lib/cadence',
      screen: true,
    })
  })
})
