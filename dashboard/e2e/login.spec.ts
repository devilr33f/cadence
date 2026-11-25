import { expect, freshValue, learn, signIn, test } from './support'

test.describe('before the page hydrates', () => {
  // no script runs: the submit is the browser's own, as when Enter comes before hydration on a slow load
  test.use({ javaScriptEnabled: false })

  test('a submit posts the password to the login page, which refuses it unread, and never puts it in the url', async ({ page }) => {
    // not the owner's: the post reaches the dashboard's server, which must answer without reading it
    const password = freshValue('cadence-e2e-prehydration')
    learn(password)
    await page.goto('/login?redirect=%2Fdevices')
    await page.getByLabel('Password').fill(password)
    const [answer] = await Promise.all([
      page.waitForResponse(r => r.request().isNavigationRequest() && r.request().method() !== 'GET'),
      page.getByRole('button', { name: 'Sign in' }).click(),
    ])
    expect(answer.request().method()).toBe('POST')
    expect(answer.status()).toBe(405)
    // booleans and the path alone: a failed matcher never prints a query that could hold the value
    for (const url of [
      answer.url(),
      page.url(),
    ]) {
      expect(new URL(url).pathname).toBe('/login')
      expect(new URL(url).search === '').toBe(true)
      expect(url.includes(password)).toBe(false)
    }
  })
})

test('refuses a wrong password inline, then signs the owner in', async ({ page }) => {
  await page.goto('/devices')
  await expect(page).toHaveURL(/\/login\?redirect=%2Fdevices$/)
  await page.getByLabel('Password').fill('not-the-owner-password')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toHaveText('Wrong password.')
  await expect(page.getByLabel('Password')).toBeFocused()
  await signIn(page)
  // the sim and the screen sim, whose claim the e2e approved before the specs
  await expect(page.getByRole('list', { name: 'Speakers by state' })).toContainText('2 online')
})

test('signs out from the account menu, and the pages ask for the password again', async ({ page }) => {
  await signIn(page)
  await page.getByRole('button', { name: 'Owner' }).click()
  await page.getByRole('menuitem', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login/)
  await page.goto('/')
  await expect(page).toHaveURL(/\/login\?redirect=%2F$/)
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
})

test('returns to the login when the session ends elsewhere, its feed closed by the server', async ({ page }) => {
  await signIn(page)
  // a page that reads nothing on a timer: only the feed can learn the session ended
  await page.getByRole('navigation').getByRole('link', { name: 'Tokens' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'API tokens' })).toBeVisible()
  await expect(page.getByRole('status').filter({ hasText: 'Live feed' })).toContainText('Live')
  // another tab's sign-out: the session revoked under the app, which learns it from its feed alone (1008)
  const ended = await page.evaluate(async () => (await fetch('/api/v1/session', {
    method: 'DELETE',
    headers: { 'X-Cadence-CSRF': '1' },
  })).status)
  expect(ended).toBe(204)
  await expect(page).toHaveURL(/\/login\?redirect=%2Ftokens$/, { timeout: 30_000 })
})
