import { expect, signIn, test } from './support'

// the screen sim, as the e2e enrolled it: sim-tv with the fake screen (CADENCE_SIM_TV_FAKE_SCREEN=1)
const SIM_TV = 'sim-tv-e2e'

test('sends the remote pad\'s keys to the screen sim', async ({ page }) => {
  await signIn(page)
  await page.getByRole('navigation').getByRole('link', { name: 'Devices' }).click()
  const row = page.getByRole('row').filter({
    has: page.getByRole('link', { name: SIM_TV, exact: true }),
  })
  await expect(row).toContainText('Online')
  await row.getByRole('link', { name: SIM_TV, exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: SIM_TV })).toBeVisible()

  const pad = page.getByRole('group', { name: 'Remote pad' })
  await expect(pad).toBeVisible()
  for (const key of ['Down', 'OK', 'Back']) {
    await pad.getByRole('button', { name: key, exact: true }).click()
    await expect(page.getByText(`${key} sent.`)).toBeVisible()
  }
  // the arrow keys while the pad has the focus
  await pad.focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByText('Right sent.')).toBeVisible()
  // the pad never raises the level (rule 7)
  await expect(pad.getByRole('button', { name: /volume up/i })).toHaveCount(0)
})
