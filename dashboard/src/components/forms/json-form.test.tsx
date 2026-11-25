// @vitest-environment jsdom
import type { JsonFormProps } from './json-form'
import { cleanup, render, screen, within } from '@testing-library/react'
import axe from 'axe-core'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/error'
import {
  ALARM_DAYS,
  ALARMS_CONFIG,
  LOGS_CONFIG,
  PLAYER_CONFIG,
  SET_ALARM,
  SOLOIST_CONFIG,
  SYSTEM_METRICS_CONFIG,
  URL_PLAY,
} from '@/test/device-schemas'
import { expectFocusOn } from '@/test/focus'
import { setupUser } from '@/test/typing'
import { JsonForm } from './json-form'

afterEach(cleanup)

function renderForm(props: Partial<JsonFormProps> & Pick<JsonFormProps, 'initial'>) {
  const onSubmit = vi.fn<JsonFormProps['onSubmit']>(async () => {})
  render(<JsonForm label="Configuration" submitLabel="Save" onSubmit={onSubmit} {...props} />)
  return {
    onSubmit,
    user: setupUser(),
  }
}

const save = () => screen.getByRole('button', { name: 'Save' })

describe('each supported type', () => {
  it('fills a nested object, a select, text and a number into the body', async () => {
    const { onSubmit, user } = renderForm({ schema: URL_PLAY, initial: {} })
    await user.type(screen.getByLabelText(/^url/), 'https://radio.example/stream')
    await user.selectOptions(screen.getByLabelText('mode'), 'overlay')
    const metadata = screen.getByRole('group', { name: 'metadata' })
    await user.type(within(metadata).getByLabelText('title'), 'News')
    await user.type(screen.getByLabelText('startMs'), '1500')
    await user.click(save())
    expect(onSubmit).toHaveBeenCalledWith({
      url: 'https://radio.example/stream',
      mode: 'overlay',
      metadata: { title: 'News' },
      startMs: 1500,
    })
  })

  it('takes prose with line breaks in a multiline box, and holds back one past its length', async () => {
    const schema = {
      type: 'object',
      properties: {
        persona: { type: 'string', format: 'multiline', maxLength: 30 },
      },
    }
    const { onSubmit, user } = renderForm({ schema, initial: {} })
    const box = screen.getByRole('textbox', { name: 'persona' })
    expect(box.tagName).toBe('TEXTAREA')
    await user.type(box, 'Говори тепло.{Enter}Шути.')
    await user.click(save())
    expect(onSubmit).toHaveBeenCalledWith({ persona: 'Говори тепло.\nШути.' })
    await user.type(box, ' Рассказывай анекдоты.')
    await user.click(save())
    expect(screen.getByText('At most 30 characters.')).toBeTruthy()
    expect(onSubmit).toHaveBeenCalledTimes(1)
  })

  it('marks a required field', () => {
    renderForm({ schema: URL_PLAY, initial: {} })
    expect(screen.getByRole('textbox', { name: 'url (required)' }).getAttribute('aria-required')).toBe('true')
    expect(screen.getByLabelText('mode').getAttribute('aria-required')).toBeNull()
  })

  it('switches a boolean, and clears it back to the device\'s own default', async () => {
    const { onSubmit, user } = renderForm({ schema: SOLOIST_CONFIG, initial: {} })
    const enabled = screen.getByRole('switch', { name: 'enabled' })
    expect(screen.getByText('Not set: the device decides')).toBeTruthy()
    await user.click(enabled)
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({ enabled: true })
    await user.click(screen.getByRole('button', { name: 'Clear enabled' }))
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({})
  })

  it('picks a fixed set of options as checkboxes, in the schema\'s order', async () => {
    const { onSubmit, user } = renderForm({ schema: ALARM_DAYS, initial: {} })
    const days = screen.getByRole('group', { name: 'days' })
    await user.click(within(days).getByLabelText('fri'))
    await user.click(within(days).getByLabelText('mon'))
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({
      days: ['mon', 'fri'],
    })
  })

  it('adds and removes free items of a list', async () => {
    const schema = {
      type: 'object',
      properties: {
        tools: {
          type: 'array',
          items: { type: 'string' },
        },
      },
    }
    const { onSubmit, user } = renderForm({
      schema,
      initial: {
        tools: ['weather'],
      },
    })
    await user.type(screen.getByLabelText('tools'), 'search{Enter}')
    await user.type(screen.getByLabelText('tools'), 'alarms')
    await user.click(screen.getByRole('button', { name: 'Add to tools' }))
    await user.click(screen.getByRole('button', { name: 'Remove weather' }))
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({
      tools: ['search', 'alarms'],
    })
  })
})

describe('what the form cannot show', () => {
  it('edits that part as json and keeps every field of it', async () => {
    const initial = {
      sources: { kernel: 'debug', wifi: 'warn' },
      kmsg: false,
    }
    const { onSubmit, user } = renderForm({ schema: LOGS_CONFIG, initial })
    const sources = screen.getByLabelText('sources') as HTMLTextAreaElement
    expect(JSON.parse(sources.value)).toEqual(initial.sources)
    expect(screen.getByText('Edited as JSON: the form cannot show this part.')).toBeTruthy()
    await user.click(screen.getByRole('switch', { name: 'thirdParty' }))
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({ ...initial, thirdParty: true })
  })

  it('keeps a value the form could not show whole', async () => {
    const { onSubmit, user } = renderForm({
      schema: PLAYER_CONFIG,
      initial: { duckMode: 'loud' },
    })
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({ duckMode: 'loud' })
  })

  it('holds back json that does not parse', async () => {
    const { onSubmit, user } = renderForm({ schema: LOGS_CONFIG, initial: {} })
    await user.type(screen.getByLabelText('sources'), '{{"kernel"')
    await user.click(save())
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole('alert').textContent).toBe('Not valid JSON.')
  })

  it('edits a schema it cannot show at all as one json document', async () => {
    const { onSubmit, user } = renderForm({
      schema: SET_ALARM,
      initial: { sessionId: 's', id: 'a1', time: '07:30' },
    })
    const editor = screen.getByLabelText('Configuration (JSON)') as HTMLTextAreaElement
    expect(JSON.parse(editor.value)).toEqual({ sessionId: 's', id: 'a1', time: '07:30' })
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({ sessionId: 's', id: 'a1', time: '07:30' })
  })
})

describe('the raw json editor (no schema yet)', () => {
  it('opens pretty-printed and sends what it holds', async () => {
    const { onSubmit, user } = renderForm({
      initial: { lights: true },
    })
    const editor = screen.getByLabelText('Configuration (JSON)') as HTMLTextAreaElement
    expect(editor.value).toBe('{\n  "lights": true\n}')
    await user.clear(editor)
    await user.type(editor, '{{"lights": false, "clock": true}')
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({ lights: false, clock: true })
  })

  it('takes an object only', async () => {
    const { onSubmit, user } = renderForm({ initial: {} })
    const editor = screen.getByLabelText('Configuration (JSON)')
    await user.clear(editor)
    await user.type(editor, '[[1]')
    await user.click(save())
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole('alert').textContent).toBe('Must be a JSON object.')
    expect(editor.getAttribute('aria-invalid')).toBe('true')
  })

  it('shows the server\'s 422 path beside it', async () => {
    const { onSubmit, user } = renderForm({
      initial: { timeZone: 'Mars/Base' },
    })
    onSubmit.mockRejectedValueOnce(new ApiError(422, { error: 'config_invalid', path: '/timeZone' }))
    await user.click(save())
    expect(screen.getByRole('alert').textContent).toBe('The server answered 422 config_invalid at /timeZone.')
  })
})

describe('validation', () => {
  it('names what is missing, at the field, and sends nothing', async () => {
    const { onSubmit, user } = renderForm({ schema: ALARMS_CONFIG, initial: {} })
    await user.click(save())
    expect(onSubmit).not.toHaveBeenCalled()
    const field = screen.getByRole('textbox', { name: 'timeZone (required)' })
    expect(field.getAttribute('aria-invalid')).toBe('true')
    expect(document.activeElement).toBe(field)
    expect(screen.getByRole('alert').textContent).toBe('Required.')
  })

  it.each<[string, string]>([
    ['5', 'At least 10.'],
    ['301', 'At most 300.'],
    ['12.5', 'Must be a whole number.'],
    ['ten', 'Must be a whole number.'],
  ])('bounds an integer: %s', async (typed, message) => {
    const { onSubmit, user } = renderForm({ schema: SYSTEM_METRICS_CONFIG, initial: {} })
    expect(screen.getByLabelText('intervalS').getAttribute('placeholder')).toBe('Default: 30')
    await user.type(screen.getByLabelText('intervalS'), typed)
    await user.click(save())
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole('alert').textContent).toBe(message)
  })

  it('checks a pattern, then clears the message once the value fits', async () => {
    const { onSubmit, user } = renderForm({ schema: SOLOIST_CONFIG, initial: {} })
    const name = screen.getByLabelText('deviceName')
    await user.type(name, 'Kitchen ')
    await user.click(save())
    expect(screen.getByRole('alert').textContent).toContain('Must match')
    await user.type(name, '2')
    expect(screen.queryByRole('alert')).toBeNull()
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({ deviceName: 'Kitchen 2' })
  })

  it('puts the server\'s 422 on the field its path names', async () => {
    const { onSubmit, user } = renderForm({
      schema: ALARMS_CONFIG,
      initial: { timeZone: 'Mars/Base' },
    })
    onSubmit.mockRejectedValueOnce(new ApiError(422, { error: 'config_invalid', path: '/timeZone' }))
    await user.click(save())
    const field = screen.getByRole('textbox', { name: 'timeZone (required)' })
    expect(field.getAttribute('aria-invalid')).toBe('true')
    expect(screen.getByRole('alert').textContent).toBe('The server refused this value (config_invalid).')
    await user.type(field, 'x')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('words any other refusal in codes beside the form', async () => {
    const { onSubmit, user } = renderForm({
      schema: ALARMS_CONFIG,
      initial: { timeZone: 'UTC' },
    })
    onSubmit.mockRejectedValueOnce(new ApiError(404, { error: 'unknown_capability' }))
    await user.click(save())
    expect(screen.getByRole('alert').textContent).toBe('The server answered 404 unknown_capability.')
  })
})

describe('an error about a whole object', () => {
  const QUIET = {
    type: 'object',
    properties: {
      quiet: {
        type: 'object',
        properties: {
          enabled: { type: 'boolean' },
          start: { type: 'string' },
        },
        required: ['enabled', 'start'],
      },
    },
    required: ['quiet'],
  }

  it('names a required object left out at its fieldset, and focuses its first control', async () => {
    const { onSubmit, user } = renderForm({ schema: QUIET, initial: {} })
    await user.click(save())
    expect(onSubmit).not.toHaveBeenCalled()
    const group = screen.getByRole('group', { name: 'quiet (required)' })
    expect(within(group).getByRole('alert').textContent).toBe('Required.')
    expect(group.getAttribute('aria-describedby')).toBe(within(group).getByRole('alert').id)
    expect(document.activeElement).toBe(within(group).getByRole('switch', { name: 'enabled (required)' }))
  })

  it.each(['/metadata', '/metadata/extra'])('shows a 422 at %s on the object\'s fieldset and focuses it', async (path) => {
    const { onSubmit, user } = renderForm({
      schema: URL_PLAY,
      initial: { url: 'https://radio.example/a' },
    })
    onSubmit.mockRejectedValueOnce(new ApiError(422, { error: 'invalid_arguments', path }))
    await user.click(save())
    const group = screen.getByRole('group', { name: 'metadata' })
    expect(within(group).getByRole('alert').textContent).toBe('The server refused this value (invalid_arguments).')
    await expectFocusOn(within(group).getByRole('textbox', { name: 'title' }))
  })

  it('sends a required switch left off as off, at the first press', async () => {
    const { onSubmit, user } = renderForm({ schema: QUIET, initial: {} })
    const group = screen.getByRole('group', { name: 'quiet (required)' })
    expect(within(group).getByText('Off')).toBeTruthy()
    await user.type(within(group).getByRole('textbox', { name: 'start (required)' }), '23:00')
    await user.click(save())
    expect(onSubmit).toHaveBeenLastCalledWith({
      quiet: { enabled: false, start: '23:00' },
    })
  })
})

describe('lists', () => {
  it('describe a set of options, and focus its first checkbox when it is wrong', async () => {
    const schema = {
      type: 'object',
      properties: {
        days: { ...(ALARM_DAYS.properties as Record<string, object>).days, description: 'The days it rings.' },
      },
      required: ['days'],
    }
    const { user } = renderForm({
      schema,
      initial: { days: [] },
    })
    const group = screen.getByRole('group', { name: 'days (required)' })
    expect(within(group).getByText('The days it rings.')).toBeTruthy()
    await user.click(save())
    expect(within(group).getByRole('alert').textContent).toBe('At least 1 item.')
    expect(document.activeElement).toBe(within(group).getByRole('checkbox', { name: 'mon' }))
  })

  it('hold back an item typed but not added', async () => {
    const schema = {
      type: 'object',
      properties: {
        tools: {
          type: 'array',
          items: { type: 'string' },
        },
      },
    }
    const { onSubmit, user } = renderForm({ schema, initial: {} })
    await user.type(screen.getByLabelText('tools'), 'search')
    await user.click(save())
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole('alert').textContent).toBe('Add the typed item, or clear it.')
  })
})

it('has no accessibility violations axe can see without layout', async () => {
  // jsdom has no canvas; axe only probes for one
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const properties = {
    ...(URL_PLAY.properties as object),
    ...(ALARM_DAYS.properties as object),
    on: { type: 'boolean' },
    tags: {
      type: 'array',
      items: { type: 'string' },
    },
    sources: (LOGS_CONFIG.properties as Record<string, unknown>).sources,
  }
  renderForm({
    schema: { ...URL_PLAY, properties },
    initial: {
      tags: ['a'],
    },
  })
  const results = await axe.run(document.body, {
    rules: {
      'color-contrast': { enabled: false },
    },
  })
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})
