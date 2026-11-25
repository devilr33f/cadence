import { expect, learn, signIn, test } from './support'

interface Started {
  id: string
  code: string
  url: string
}

// the screen's side through the edge, from the page: /v2/claim is the server's, unauthenticated, as a tv calls it
test('approves a screen\'s claim with a name and a room, and the screen\'s poll takes its token', async ({ page }) => {
  await signIn(page)
  const started = await page.evaluate(async () => {
    const r = await fetch('/v2/claim', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ board: 'sim-tv' }),
    })
    return { status: r.status, body: await r.json() as Started }
  })
  expect(started.status).toBe(201)
  // the claim's id is the screen's bearer: no log or artifact may hold it
  learn(started.body.id)
  expect(started.body.code).toMatch(/^[2-9A-HJ-NP-Z]{6}$/)
  expect(new URL(started.body.url).pathname).toBe(`/claims/${started.body.code}`)

  const name = `claim-e2e-${Date.now()}`
  let deviceId = ''
  try {
    await page.goto(new URL(started.body.url).pathname)
    await expect(page.getByText(`${started.body.code.slice(0, 3)} · ${started.body.code.slice(3)}`, { exact: true })).toBeVisible()
    await page.getByRole('textbox', { name: 'Name' }).fill(name)
    await page.getByRole('combobox', { name: 'Room' }).fill('E2E room')
    await page.getByRole('button', { name: 'Approve the screen' }).click()
    const approved = page.getByRole('status', { name: 'Approved' })
    await expect(approved).toContainText('Approved — the screen connects by itself')
    deviceId = (await approved.getByRole('link', { name: `Open ${name}` }).getAttribute('href'))?.split('/devices/')[1] ?? ''
    expect(deviceId).not.toBe('')

    // the screen's next poll: approved, with the device's token, once. only booleans leave the page
    const taken = await page.evaluate(async ({ id, deviceId }) => {
      const r = await fetch(`/v2/claim/${id}?wait=5s`)
      const body = await r.json() as { state: string, deviceId?: string, token?: string }
      const again = await fetch(`/v2/claim/${id}?wait=0s`)
      return {
        status: r.status,
        state: body.state,
        sameDevice: body.deviceId === deviceId,
        hasToken: typeof body.token === 'string' && body.token.startsWith('cdd_'),
        token: body.token ?? '',
        againStatus: again.status,
      }
    }, { id: started.body.id, deviceId })
    learn(taken.token)
    expect(taken.status).toBe(200)
    expect(taken.state).toBe('approved')
    expect(taken.sameDevice).toBe(true)
    expect(taken.hasToken).toBe(true)
    expect(taken.againStatus).toBe(404)

    // the device is enrolled, in its room
    await page.goto(`/devices/${deviceId}`)
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible()
    await expect(page.getByText('E2E room')).toBeVisible()
  }
  finally {
    if (deviceId !== '') {
      await page.evaluate(async (id) => {
        await fetch(`/api/v1/devices/${id}`, {
          method: 'DELETE',
          headers: { 'X-Cadence-CSRF': '1' },
        })
      }, deviceId)
    }
  }
})

test('declines a claim, which the screen\'s poll then says', async ({ page }) => {
  await signIn(page)
  const started = await page.evaluate(async () => {
    const r = await fetch('/v2/claim', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ board: 'sim-tv' }),
    })
    return await r.json() as Started
  })
  learn(started.id)
  await page.getByRole('navigation').getByRole('link', { name: 'Devices' }).click()
  await page.getByRole('status').getByRole('link', { name: 'Screens waiting' }).click()
  const label = `${started.code.slice(0, 3)} · ${started.code.slice(3)}`
  await page.getByRole('button', { name: `Decline ${label}` }).click()
  await expect(page.getByRole('button', { name: `Decline ${label}` })).toBeHidden()
  const state = await page.evaluate(async id => (await (await fetch(`/v2/claim/${id}?wait=0s`)).json() as { state: string }).state, started.id)
  expect(state).toBe('declined')
})
