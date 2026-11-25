import { describe, expect, it } from 'vitest'
import { settingGroups, settingText } from './settings-catalog'

function keysOf(scope: 'global' | 'device'): string[] {
  return settingGroups(scope).flatMap(group => group.settings.map(spec => spec.key)).sort()
}

// every key of the server's settings (SettingsReply) but those the scope cannot set: the server answers
// settings_invalid for a device's global-only key and for the global haArea
describe('the settings each scope offers', () => {
  it('are every key but haArea globally', () => {
    expect(keysOf('global')).toEqual([
      'afterBusy',
      'alarms',
      'allowedModes',
      'busyEntities',
      'catalogSources',
      'catalogVoiceovers',
      'compactAfter',
      'compactionMaxTokens',
      'compactionMode',
      'embed',
      'errorSound',
      'followUp',
      'haFavorites',
      'haSensitive',
      'hermes',
      'hermesApprovals',
      'historyTurns',
      'homeAssistant',
      'language',
      'llm',
      'logRetentionDays',
      'maintenanceModel',
      'mcp',
      'memoryApproval',
      'memoryMode',
      'memoryScopes',
      'mode',
      'modeRevert',
      'model',
      'music',
      'musicPlayer',
      'persona',
      'quietHours',
      'remoteTargets',
      'search',
      'stt',
      'subscriptionApprovals',
      'subscriptionDelivery',
      't3code',
      'telemetryExport',
      'timeZone',
      'tools',
      'tracing',
      'tts',
      'voice',
      'weather',
      'weatherLocation',
      'xSearch',
      'youtubeVotes',
    ])
  })

  it('leave the global-only keys out on a device', () => {
    const device = keysOf('device')
    expect(device).toHaveLength(37)
    expect(device).toContain('haArea')
    for (const key of [
      'hermes',
      'homeAssistant',
      'haSensitive',
      'haFavorites',
      'mcp',
      't3code',
      'logRetentionDays',
      'telemetryExport',
      'tracing',
      'busyEntities',
      'catalogSources',
      'catalogVoiceovers',
      'youtubeVotes',
    ])
      expect(device).not.toContain(key)
  })

  it('list no group the scope has nothing in', () => {
    for (const scope of ['global', 'device'] as const)
      expect(settingGroups(scope).every(group => group.settings.length > 0)).toBe(true)
  })
})

describe('the catalog voiceovers row', () => {
  const spec = settingGroups('global').flatMap(group => group.settings).find(s => s.key === 'catalogVoiceovers')!
  it('reads the preferred order and the avoided ones', () => {
    expect(settingText(spec, {
      prefer: ['AniLibria', 'anime365'],
      avoid: ['animevost'],
    })).toBe('prefer AniLibria → anime365; avoid animevost')
    expect(settingText(spec, { prefer: [] })).toBe('none')
    expect(settingText(spec, undefined)).toContain('asks')
  })
})
