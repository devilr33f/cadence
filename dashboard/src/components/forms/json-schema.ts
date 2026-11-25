import { isObject, pointerOf, segmentsOf } from './json-pointer'

// a json schema (draft 2020-12) as the server passes it on: read for the subset the renderer shows
export type JsonSchema = Record<string, unknown>

interface Base {
  path: readonly string[]
  pointer: string
  name: string
  required: boolean
  schema: JsonSchema
}

export type ScalarNode
  = | Base & { kind: 'string' }
    | Base & { kind: 'enum', options: readonly string[] }
    | Base & { kind: 'integer' | 'number' }

export type FieldNode
  = | ScalarNode
    | Base & { kind: 'boolean' }
    | Base & { kind: 'tags', item: ScalarNode }
    | Base & { kind: 'object', fields: readonly FieldNode[] }
    // a part the form cannot show, edited as json so no field of it is lost
    | Base & { kind: 'raw' }

// keywords that say nothing a form must enforce
const ANNOTATIONS = [
  '$schema',
  '$comment',
  'title',
  'description',
  'default',
  'examples',
  'deprecated',
  'readOnly',
  'writeOnly',
]

const KEYWORDS: Record<string, readonly string[]> = {
  enum: ['type', 'enum'],
  string: [
    'type',
    'minLength',
    'maxLength',
    'pattern',
    'format',
  ],
  integer: [
    'type',
    'minimum',
    'maximum',
    'exclusiveMinimum',
    'exclusiveMaximum',
  ],
  number: [
    'type',
    'minimum',
    'maximum',
    'exclusiveMinimum',
    'exclusiveMaximum',
  ],
  boolean: ['type'],
  array: [
    'type',
    'items',
    'minItems',
    'maxItems',
    'uniqueItems',
  ],
  object: [
    'type',
    'properties',
    'required',
    'additionalProperties',
  ],
}

const TYPES = [
  'string',
  'integer',
  'number',
  'boolean',
  'array',
  'object',
]

function onlyKnown(schema: JsonSchema, kind: string): boolean {
  const allowed = KEYWORDS[kind]!
  return Object.keys(schema).every(key => allowed.includes(key) || ANNOTATIONS.includes(key))
}

function compiles(pattern: unknown): boolean {
  if (pattern === undefined)
    return true
  if (typeof pattern !== 'string')
    return false
  try {
    void new RegExp(pattern, 'u')
    return true
  }
  catch {
    return false
  }
}

function isCount(value: unknown): boolean {
  return value === undefined || (typeof value === 'number' && Number.isInteger(value) && value >= 0)
}

function isBound(value: unknown): boolean {
  return value === undefined || (typeof value === 'number' && Number.isFinite(value))
}

// the kind a schema reads as, or raw when anything in it is outside the subset
function kindOf(schema: JsonSchema): ScalarNode['kind'] | 'boolean' | 'array' | 'object' | 'raw' {
  const { type, enum: options } = schema
  if (options !== undefined) {
    const strings = Array.isArray(options) && options.length > 0 && options.every(o => typeof o === 'string')
    return strings && (type === undefined || type === 'string') && onlyKnown(schema, 'enum')
      ? 'enum'
      : 'raw'
  }
  if (typeof type !== 'string' || !TYPES.includes(type) || !onlyKnown(schema, type))
    return 'raw'
  switch (type) {
    case 'string':
      return isCount(schema.minLength) && isCount(schema.maxLength) && compiles(schema.pattern)
        ? 'string'
        : 'raw'
    case 'integer':
    case 'number':
      return [
        schema.minimum,
        schema.maximum,
        schema.exclusiveMinimum,
        schema.exclusiveMaximum,
      ].every(isBound)
        ? type
        : 'raw'
    case 'array':
      return isCount(schema.minItems) && isCount(schema.maxItems) && (schema.uniqueItems === undefined || typeof schema.uniqueItems === 'boolean')
        ? 'array'
        : 'raw'
    case 'object': {
      const { properties, required, additionalProperties } = schema
      const fine = (properties === undefined || isObject(properties))
        && (required === undefined || (Array.isArray(required) && required.every(r => typeof r === 'string' && isObject(properties) && Object.hasOwn(properties, r))))
        && (additionalProperties === undefined || typeof additionalProperties === 'boolean')
      return fine ? 'object' : 'raw'
    }
    default:
      return 'boolean'
  }
}

function fitsScalar(kind: ScalarNode['kind'], schema: JsonSchema, value: unknown): boolean {
  if (value === undefined)
    return true
  switch (kind) {
    case 'string':
      return typeof value === 'string'
    case 'enum':
      return typeof value === 'string' && (schema.enum as string[]).includes(value)
    case 'integer':
      return typeof value === 'number' && Number.isInteger(value)
    case 'number':
      return typeof value === 'number' && Number.isFinite(value)
  }
}

function scalar(base: Base, kind: ScalarNode['kind']): ScalarNode {
  return kind === 'enum'
    ? { ...base, kind, options: base.schema.enum as string[] }
    : { ...base, kind }
}

interface At {
  path: readonly string[]
  name: string
  required: boolean
}

function build(schema: unknown, value: unknown, at: At): FieldNode {
  const base = {
    ...at,
    pointer: pointerOf(at.path),
    schema: isObject(schema)
      ? schema
      : {},
  }
  if (!isObject(schema))
    return { ...base, kind: 'raw' }
  const raw: FieldNode = { ...base, kind: 'raw' }
  const kind = kindOf(schema)
  switch (kind) {
    case 'raw':
      return raw
    case 'boolean':
      return value === undefined || typeof value === 'boolean'
        ? { ...base, kind }
        : raw
    case 'array': {
      const items = schema.items
      const itemKind = isObject(items)
        ? kindOf(items)
        : 'raw'
      if (itemKind !== 'string' && itemKind !== 'enum' && itemKind !== 'integer' && itemKind !== 'number')
        return raw
      const fits = value === undefined || (Array.isArray(value) && value.every(v => fitsScalar(itemKind, items as JsonSchema, v)))
      if (!fits)
        return raw
      const item = scalar({
        ...at,
        path: at.path,
        pointer: base.pointer,
        required: true,
        schema: items as JsonSchema,
      }, itemKind)
      return { ...base, kind: 'tags', item }
    }
    case 'object': {
      const properties = (schema.properties ?? {}) as Record<string, unknown>
      const required = (schema.required ?? []) as string[]
      if (value !== undefined && !(isObject(value) && Object.keys(value).every(key => Object.hasOwn(properties, key))))
        return raw
      const fields = Object.entries(properties).map(([name, child]) => build(child, isObject(value)
        ? value[name]
        : undefined, {
        path: [...at.path, name],
        name,
        required: required.includes(name),
      }))
      return { ...base, kind, fields }
    }
    default:
      return fitsScalar(kind, schema, value)
        ? scalar(base, kind)
        : raw
  }
}

// the form for a schema and the value it opens with; a value the form could not show whole makes its part raw
export function buildForm(schema: unknown, value: unknown): FieldNode {
  return build(schema, value, { path: [], name: '', required: false })
}

// the field the server's 422 path is about: the deepest one whose pointer leads to it (a list's item, a raw part's
// inside); none for the whole document or a key the form has no field for
export function fieldFor(root: FieldNode, pointer: string): FieldNode | undefined {
  const target = segmentsOf(pointer)
  let found: FieldNode | undefined
  let node: FieldNode | undefined = root
  while (node) {
    if (node.path.length > 0)
      found = node
    if (node.kind !== 'object' || node.path.length === target.length)
      break
    const next: string | undefined = target[node.path.length]
    node = node.fields.find(field => field.name === next)
  }
  return found && found.path.every((segment, i) => target[i] === segment)
    ? found
    : undefined
}
