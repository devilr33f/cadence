import type { Page } from '@playwright/test'
import { expect, learn, pageHolds, signIn, test } from './support'

const NAME = 'dashboard-e2e'

async function openTokens(page: Page) {
  await signIn(page)
  await page.getByRole('navigation').getByRole('link', { name: 'Tokens' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'API tokens' })).toBeVisible()
}

function openCreate(page: Page) {
  return page.getByRole('button', { name: 'Create a token' }).first().click()
}

test('creates a token shown once, gone once its dialog closes', async ({ page }) => {
  await openTokens(page)
  await openCreate(page)
  const dialog = page.getByRole('dialog', { name: 'Create a token' })
  await dialog.getByRole('textbox', { name: 'Name (required)' }).fill(NAME)
  await dialog.getByRole('button', { name: 'Create the token' }).click()
  const once = page.getByRole('dialog', { name: 'Copy the new token' })
  const token = (await once.getByRole('figure', { name: `Token for ${NAME}` }).locator('code').textContent())?.trim() ?? ''
  learn(token)
  expect(token.startsWith('cdn_')).toBe(true)
  await once.getByRole('button', { name: 'Done' }).click()
  // waited for without a locator on the dialog: a failed matcher would write the dialog, token and all, into its error
  await page.waitForFunction(() => document.querySelector('[role=dialog]') === null)
  expect(await pageHolds(page, token)).toBe(false)
  await expect(page.getByRole('row').filter({ hasText: NAME })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('row').filter({ hasText: NAME })).toBeVisible()
  expect(await pageHolds(page, token)).toBe(false)
})

test('keeps the focus in the dialog through a refusal: on the field it names, else on its submit', async ({ page }) => {
  await openTokens(page)
  await openCreate(page)
  const dialog = page.getByRole('dialog', { name: 'Create a token' })
  const name = dialog.getByRole('textbox', { name: 'Name (required)' })
  const submit = dialog.getByRole('button', { name: 'Create the token' })
  // the first test's token holds the name: the server's 409 is the name's
  await name.fill(NAME)
  await submit.click()
  await expect(dialog.getByText('The server answered 409 name_taken.')).toBeVisible()
  await expect(name).toBeFocused()
  // a refusal of no field (the server away): the message beside the form, the focus still on the submit that sent it
  await page.route('**/api/v1/tokens', route => route.request().method() === 'POST'
    ? route.fulfill({
        status: 503,
        json: { error: 'unavailable' },
      })
    : route.fallback())
  await name.fill(`${NAME}-2`)
  await submit.click()
  await expect(dialog.getByRole('alert')).toHaveText('The server answered 503 unavailable.')
  await expect(submit).toBeFocused()
  await page.unroute('**/api/v1/tokens')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toBeHidden()
})

test('revokes the token', async ({ page }) => {
  await openTokens(page)
  await page.getByRole('button', { name: `Revoke ${NAME}` }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Revoke the token' }).click()
  await expect(page.getByRole('row').filter({ hasText: NAME })).toHaveCount(0)
})
