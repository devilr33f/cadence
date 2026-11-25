import { askForLogLine, expect, keptPastPage, signIn, test } from './support'

test('tails a line of the sim\'s log probe live, held in memory only', async ({ page }) => {
  await signIn(page)
  await page.getByRole('navigation').getByRole('link', { name: 'Logs' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Logs' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Stored lines' })).toBeVisible()
  const live = page.getByRole('switch', { name: 'Live' })
  await live.click()
  await expect(page.getByRole('status', { name: 'Live tail' })).toHaveText('Following new lines.')
  const lines = page.getByRole('region', { name: 'Live lines' })
  await expect(lines).toBeVisible()

  // no secret, so the locators below may name it: the privacy block greps every log for the probe's prefix
  const line = `cadence-e2e-logprobe dashboard-${crypto.randomUUID()}`
  askForLogLine(line)
  await expect(lines.getByText(line, { exact: true })).toBeVisible({ timeout: 45_000 })
  await expect(lines.getByRole('listitem').filter({ hasText: line })).toContainText('sim-probe')
  expect(await keptPastPage(page, line)).toBe(false)

  // off, the tail goes and the stored lines come back
  await live.click()
  await expect(lines).toBeHidden()
  await expect(page.getByRole('region', { name: 'Stored lines' })).toBeVisible()
})
