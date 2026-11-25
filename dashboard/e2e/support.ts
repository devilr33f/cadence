import type { BrowserContext, Locator, Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import process from 'node:process'
import { test as base, expect } from '@playwright/test'

export { expect }

// what the specs tell deploy/dev/playwright.sh on their output, which it takes out before the output is shown
const MARK = 'cadence-e2e:'

// the owner's password, which the image's command wrote from its stdin into the container's own /tmp
export function ownerPassword(): string {
  const file = process.env.CADENCE_E2E_PASSWORD_FILE
  if (!file)
    throw new Error('CADENCE_E2E_PASSWORD_FILE is not set')
  return readFileSync(file, 'utf8').trim()
}

// what a spec saw that no log or artifact may carry (the session's cookies, a token, a secret it typed): the runner
// hands each to the e2e, which greps every log and artifact for it
export function learn(...values: string[]) {
  for (const value of values.filter(v => v !== ''))
    process.stdout.write(`${MARK}learned ${value}\n`)
}

// asks the runner for one turn on the sim (its say file); the timeline then shows it
export function askForTurn() {
  process.stdout.write(`${MARK}want-turn\n`)
}

// asks the runner for one line of the sim's log probe, which the sim ships as source sim-probe: letters, digits, spaces,
// "_" and "-" only, as the runner writes nothing else into the probe file
export function askForLogLine(text: string) {
  if (!/^[\w -]{1,200}$/.test(text))
    throw new Error('a probe line holds letters, digits, spaces, "_" and "-" only')
  process.stdout.write(`${MARK}want-logprobe ${text}\n`)
}

// a sign-in's cookies, learned apart from other values, so the runner can tell each sign-in reported both
async function learnCookies(context: BrowserContext) {
  for (const { value } of (await context.cookies()).filter(c => c.name.includes('cadence_') && c.value !== ''))
    process.stdout.write(`${MARK}learned-cookie ${value}\n`)
}

// a value no page should ever show back: a secret typed into a field, then looked for in the page
export function freshValue(prefix: string): string {
  return `${prefix}-${crypto.randomUUID()}`
}

// whether the page holds the value anywhere: its html or a field's value. a boolean, so a failure never prints it
export async function pageHolds(page: Page, value: string): Promise<boolean> {
  return page.evaluate(v => document.documentElement.outerHTML.includes(v)
    || [
      ...document.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input, textarea'),
    ].some(f => f.value.includes(v)), value)
}

// whether the browser keeps the value past the page: its url, its storage or a cookie a script can read (a transcript, a
// log line). a boolean, so a failure never prints it
export async function keptPastPage(page: Page, value: string): Promise<boolean> {
  return page.evaluate(v => window.location.href.includes(v)
    || document.cookie.includes(v)
    || [
      localStorage,
      sessionStorage,
    ].some(s => Object.keys(s).some(k => k.includes(v) || (s.getItem(k) ?? '').includes(v))), value)
}

// whether the elements hold the text (whitespace runs read as one space): a boolean, for an element that shows the
// fake transcript, which a failed matcher would write with the received text into its error and the artifacts
export async function holdsText(locator: Locator, text: string): Promise<boolean> {
  return locator.evaluateAll((elements, t) => elements.some(e => (e.textContent ?? '').replace(/\s+/g, ' ').includes(t)), text)
}

export async function signIn(page: Page) {
  await page.goto('/login')
  await page.getByLabel('Password').fill(ownerPassword())
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Speakers' })).toBeVisible()
  await learnCookies(page.context())
  // the runner holds each sign-in to its two cookies learned: a marker it cannot read would leave them out of the greps
  process.stdout.write(`${MARK}signed-in\n`)
}

export const test = base.extend<{ failureShot: void }>({
  // a failure's screenshot, every field and the one-time token box masked
  failureShot: [
    async ({ page }, use, testInfo) => {
      await use()
      if (testInfo.status === testInfo.expectedStatus)
        return
      await page.screenshot({
        path: testInfo.outputPath('failure.png'),
        fullPage: true,
        mask: [
          page.locator('input, textarea, pre'),
        ],
      }).catch(() => {})
    },
    { auto: true },
  ],
})
