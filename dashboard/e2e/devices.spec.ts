import type { Page } from '@playwright/test'
import { openSim, SIM, TRANSCRIPT } from './sim'
import { askForTurn, expect, freshValue, holdsText, learn, pageHolds, test } from './support'

interface Generations {
  desiredGen: number
  appliedGen: number
  appliedError: string
}

// a capability's generations as the api has them, read by the page with its own session
async function generations(page: Page, capability: string): Promise<Generations> {
  const id = page.url().split('/devices/')[1]!
  return page.evaluate(async ({ id, capability }) => {
    const detail = await (await fetch(`/api/v1/devices/${id}`)).json() as { capabilities: Array<Generations & { id: string }> }
    const { desiredGen, appliedGen, appliedError } = detail.capabilities.find(c => c.id === capability)!
    return { desiredGen, appliedGen, appliedError }
  }, { id, capability })
}

test('lists the sim online and shows its page', async ({ page }) => {
  await openSim(page)
  await expect(page.getByRole('heading', { level: 1, name: SIM }).locator('..')).toContainText('Online')
  await expect(page.getByText('Board', { exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'audio.output' })).toBeVisible()
})

test('changes a capability\'s config through its schema\'s form, desired to applied on the sim', async ({ page }) => {
  await openSim(page)
  const card = page.getByRole('region', { name: 'system.metrics' })
  const before = await generations(page, 'system.metrics')
  const interval = (await card.textContent())?.includes('intervalS20')
    ? 15
    : 20
  await card.getByRole('button', { name: 'Edit the configuration' }).click()
  // the form of the config schema the sim declares (its integer typed as text), not the raw json editor
  const field = card.getByRole('textbox', { name: 'intervalS', exact: true })
  await expect(field).toBeFocused()
  await expect(card.getByRole('textbox', { name: 'Configuration of system.metrics (JSON)' })).toHaveCount(0)
  await field.fill(String(interval))
  await card.getByRole('button', { name: 'Save the configuration' }).click()
  await expect(card.getByRole('button', { name: 'Edit the configuration' })).toBeFocused()
  await expect(card).toContainText(new RegExp(String.raw`Applied\s*gen ${before.desiredGen + 1}(?!\d)`), { timeout: 30_000 })
  await expect(card).toContainText(`intervalS${interval}`)
  expect(await generations(page, 'system.metrics')).toEqual({
    desiredGen: before.desiredGen + 1,
    appliedGen: before.desiredGen + 1,
    appliedError: '',
  })
})

test('sets a secret, which reaches the sim and then shows as set, never as its value', async ({ page }) => {
  await openSim(page)
  const card = page.getByRole('region', { name: 'media.soloist' })
  const row = card.getByRole('listitem').filter({ hasText: 'apiKey' })
  // the e2e set the key before, so "Set" alone proves nothing: the save is the new generation the sim applies
  const before = await generations(page, 'media.soloist')
  const value = freshValue('cadence-e2e-dashboard-key')
  learn(value)
  await row.getByRole('button', { name: /^(?:Set|Replace) apiKey$/ }).click()
  await row.getByLabel('New value for apiKey').fill(value)
  await row.getByRole('button', { name: 'Save the secret' }).click()
  await expect(row.getByRole('button', { name: 'Replace apiKey' })).toBeFocused()
  await expect.poll(async () => (await generations(page, 'media.soloist')).desiredGen).toBe(before.desiredGen + 1)
  await expect.poll(() => generations(page, 'media.soloist'), { timeout: 30_000 }).toEqual({
    desiredGen: before.desiredGen + 1,
    appliedGen: before.desiredGen + 1,
    appliedError: '',
  })
  await expect(card).toContainText(new RegExp(String.raw`Applied\s*gen ${before.desiredGen + 1}(?!\d)`), { timeout: 30_000 })
  await expect(row).toContainText('Set')
  expect(await pageHolds(page, value)).toBe(false)
  await page.reload()
  await expect(page.getByRole('region', { name: 'media.soloist' }).getByRole('listitem').filter({ hasText: 'apiKey' })).toContainText('Set')
  expect(await pageHolds(page, value)).toBe(false)
})

test('shows a turn the sim takes on its live timeline', async ({ page }) => {
  await openSim(page)
  await expect(page.getByRole('status').filter({ hasText: 'Live feed' })).toContainText('Live')
  askForTurn()
  const timeline = page.getByRole('list', { name: `${SIM} activity` })
  // booleans: the timeline shows the transcript, which a failed matcher would print with its text
  const heard = `Heard: «${TRANSCRIPT}»`
  // the say file starts a button session: the feed's trigger is named, not the bare fallback
  await expect.poll(() => holdsText(timeline, 'Woken by a press'), { timeout: 45_000 }).toBe(true)
  await expect.poll(() => holdsText(timeline, heard), { timeout: 45_000 }).toBe(true)
  await expect.poll(() => holdsText(timeline, 'Turn ended'), { timeout: 30_000 }).toBe(true)
})
