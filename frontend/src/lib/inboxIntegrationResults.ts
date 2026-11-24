import type { FieldDefinition, FieldValue } from '@/services/inbox'
import type { IntegrationDefinition } from '@/services/inboxIntegrations'

export interface InvestigatedField {
  name: string
  field?: FieldDefinition
  reference: boolean
}

// Only the current declared schema is authorable. The server separately binds
// the review to the original execution and rejects changed or forbidden fields.
export function investigatedSchema(
  definition: IntegrationDefinition,
  fields: FieldDefinition[],
) {
  if (definition.outputs.length > 16) return undefined
  const byId = new Map(fields.map((field) => [field.id, field]))
  const selected = new Map<string, InvestigatedField>()
  for (const output of definition.outputs) {
    const field = byId.get(output.definition_id)
    if (
      !field ||
      field.archived_at ||
      !['text', 'number', 'date', 'boolean', 'select'].includes(field.kind) ||
      !['case', 'contact'].includes(field.scope) ||
      selected.has(output.name)
    )
      return undefined
    selected.set(output.name, { name: output.name, field, reference: false })
  }
  if (definition.reference_key) {
    const existing = selected.get(definition.reference_key)
    if (existing?.field && ['number', 'boolean'].includes(existing.field.kind))
      return undefined
    selected.set(definition.reference_key, {
      name: definition.reference_key,
      field: existing?.field,
      reference: true,
    })
  }
  return [...selected.values()]
}

export function parseInvestigatedResults(
  schema: InvestigatedField[],
  raw: Record<string, string | number>,
) {
  const result: Record<string, Exclude<FieldValue, null>> = Object.create(null)
  const errors: Record<string, string> = Object.create(null)
  for (const entry of schema) {
    // Read even an absent key so Vue tracks a subsequently authored value.
    const candidate = raw[entry.name]
    const authored = Object.prototype.hasOwnProperty.call(raw, entry.name)
      ? candidate
      : ''
    const value =
      typeof authored === 'number'
        ? String(authored)
        : typeof authored === 'string'
          ? authored
          : ''
    if (!value.trim()) {
      errors[entry.name] = 'required'
      continue
    }
    const kind = entry.field?.kind || 'text'
    if (kind === 'number') {
      if (
        !/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(value.trim()) ||
        !Number.isFinite(Number(value))
      )
        errors[entry.name] = 'number'
      else result[entry.name] = Number(value)
    } else if (kind === 'boolean') {
      if (value !== 'true' && value !== 'false') errors[entry.name] = 'boolean'
      else result[entry.name] = value === 'true'
    } else if (kind === 'date') {
      const date = new Date(value + 'T00:00:00.000Z')
      if (
        !/^\d{4}-\d{2}-\d{2}$/.test(value) ||
        !Number.isFinite(date.valueOf()) ||
        date.toISOString().slice(0, 10) !== value
      )
        errors[entry.name] = 'date'
      else result[entry.name] = value
    } else if (kind === 'select') {
      if (!entry.field?.options?.includes(value)) errors[entry.name] = 'option'
      else result[entry.name] = value
    } else if (kind === 'text') {
      if (Array.from(value).length > 4096) errors[entry.name] = 'text'
      else result[entry.name] = value
    } else errors[entry.name] = 'schema'
    if (
      entry.reference &&
      (new TextEncoder().encode(value).length > 200 || /[\r\n\0]/.test(value))
    )
      errors[entry.name] = 'reference'
  }
  const tooLarge =
    new TextEncoder().encode(JSON.stringify(result)).length > 65536
  return {
    result: !tooLarge && !Object.keys(errors).length ? result : undefined,
    errors,
    tooLarge,
  }
}

export function validInvestigationReference(value: string) {
  return (
    !!value.trim() &&
    Array.from(value.trim()).length <= 200 &&
    !/[\r\n\0]/.test(value)
  )
}
