// what a keyboard can land on inside a form: fields, the switch's span, buttons
const CONTROLS = 'input:not([type=hidden]), select, textarea, button, [role="switch"], [tabindex]:not([tabindex="-1"])'

export function firstControl(within: Element | null): HTMLElement | null {
  return within?.querySelector<HTMLElement>(CONTROLS) ?? null
}

// the first field whose error shows: a control marked invalid, or a group (an object's fieldset, a list's options)
// whose first control then takes the focus
export function focusFirstInvalid(form: HTMLElement | null) {
  const target = form?.querySelector<HTMLElement>('[aria-invalid="true"], [data-invalid="true"]')
  if (!target)
    return
  if (target.matches(CONTROLS))
    target.focus()
  else
    firstControl(target)?.focus()
}
