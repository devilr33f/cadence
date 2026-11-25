import type { Page } from '@playwright/test'
import { expect, signIn } from './support'

// the sim's name, as the e2e enrolled it
export const SIM = 'sim-e2e'

// what the fake speech to text hears in every turn of the sim. kept here, away from the specs' assertions: a failing
// assertion's code frame lands in its error-context.md, and an element that shows this text is checked with holdsText
// (a boolean) only, so no artifact holds it
export const TRANSCRIPT = 'what time is it'

// the sim's page, reached as the owner would: the overview first, then the list, which says whether it is online
export async function openSim(page: Page) {
  await signIn(page)
  await page.getByRole('navigation').getByRole('link', { name: 'Devices' }).click()
  const row = page.getByRole('row').filter({
    has: page.getByRole('link', { name: SIM }),
  })
  await expect(row).toContainText('Online')
  await row.getByRole('link', { name: SIM }).click()
  await expect(page.getByRole('heading', { level: 1, name: SIM })).toBeVisible()
}
