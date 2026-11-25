import { createContext, use } from 'react'

export interface FormFields {
  root: unknown
  set: (path: readonly string[], value: unknown) => void
  // the message a field shows now: the server's for it, else the form's own once a submit was tried
  errorOf: (pointer: string) => string | undefined
  // a field's own text that is no value yet (a number half typed, json that does not parse)
  reportLocal: (pointer: string, error: string | undefined) => void
  idOf: (pointer: string) => string
}

export const FormFieldsContext = createContext<FormFields | null>(null)

export function useFormFields(): FormFields {
  const fields = use(FormFieldsContext)
  if (!fields)
    throw new Error('a form field is used outside its form')
  return fields
}
