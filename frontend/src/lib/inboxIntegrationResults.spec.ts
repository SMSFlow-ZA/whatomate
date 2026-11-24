import { expect, it } from 'vitest'
import { computed, reactive } from 'vue'
import type { FieldDefinition } from '@/services/inbox'
import type { IntegrationDefinition } from '@/services/inboxIntegrations'
import {
  investigatedSchema,
  parseInvestigatedResults,
  validInvestigationReference,
} from './inboxIntegrationResults'

const field = (
  kind: FieldDefinition['kind'],
  id: string = kind,
): FieldDefinition => ({
  id,
  kind,
  label: id,
  key: id,
  scope: 'case',
  required_on_resolve: false,
  options: ['Listed'],
})
const definition = (
  outputs = [{ name: 'score', definition_id: 'number' }],
  reference_key = '',
) => ({ outputs, reference_key }) as IntegrationDefinition

it('keeps false and zero as explicitly authored typed values', () => {
  const schema = investigatedSchema(
    definition([
      { name: 'score', definition_id: 'number' },
      { name: 'active', definition_id: 'boolean' },
    ]),
    [field('number'), field('boolean')],
  )!
  expect(
    parseInvestigatedResults(schema, { score: '0', active: 'false' }).result,
  ).toEqual({ score: 0, active: false })
  expect(
    parseInvestigatedResults(schema, { score: 0, active: 'false' }).result,
  ).toEqual({ score: 0, active: false })
  expect(
    parseInvestigatedResults(schema, { score: '', active: '' }).result,
  ).toBeUndefined()
})
it('recomputes when a previously absent form value is authored', () => {
  const schema = investigatedSchema(definition(), [field('number')])!
  const raw = reactive<Record<string, string | number>>(Object.create(null))
  const selected = computed(() => parseInvestigatedResults(schema, raw))
  expect(selected.value.result).toBeUndefined()
  raw.score = 0
  expect(selected.value.result).toEqual({ score: 0 })
})

it('rejects missing, archived, duplicate and impossible shared-reference schemas', () => {
  expect(investigatedSchema(definition(), [])).toBeUndefined()
  expect(
    investigatedSchema(definition(), [
      { ...field('number'), archived_at: '2026-01-01' },
    ]),
  ).toBeUndefined()
  expect(
    investigatedSchema(
      definition([
        { name: 'score', definition_id: 'number' },
        { name: 'score', definition_id: 'number' },
      ]),
      [field('number')],
    ),
  ).toBeUndefined()
  expect(
    investigatedSchema(definition(undefined, 'score'), [field('number')]),
  ).toBeUndefined()
})
it('shares a declared string reference with its mapped field without creating duplicate inputs', () => {
  const schema = investigatedSchema(
    definition([{ name: 'reference', definition_id: 'text' }], 'reference'),
    [field('text')],
  )!
  expect(schema).toHaveLength(1)
  expect(
    parseInvestigatedResults(schema, { reference: 'CASE-1' }).result,
  ).toEqual({ reference: 'CASE-1' })
  expect(
    parseInvestigatedResults(schema, { reference: 'CASE\n1' }).result,
  ).toBeUndefined()
})
it('validates real dates and finite decimal numbers without evaluating expressions', () => {
  const schema = investigatedSchema(
    definition([
      { name: 'date', definition_id: 'date' },
      { name: 'number', definition_id: 'number' },
    ]),
    [field('date'), field('number')],
  )!
  for (const date of ['2026-02-30', '2026-2-03', '2026-13-01'])
    expect(
      parseInvestigatedResults(schema, { date, number: '1' }).result,
    ).toBeUndefined()
  for (const number of ['Infinity', '1e999', '0x10', '1+1', 'NaN'])
    expect(
      parseInvestigatedResults(schema, { date: '2028-02-29', number }).result,
    ).toBeUndefined()
  expect(
    parseInvestigatedResults(schema, { date: '2028-02-29', number: '-1.5e2' })
      .result,
  ).toEqual({ date: '2028-02-29', number: -150 })
})
it('requires listed select values and bounds text by Unicode characters without truncating', () => {
  const schema = investigatedSchema(
    definition([
      { name: 'text', definition_id: 'text' },
      { name: 'option', definition_id: 'select' },
    ]),
    [field('text'), field('select')],
  )!
  expect(
    parseInvestigatedResults(schema, {
      text: '😀'.repeat(4096),
      option: 'Listed',
    }).result?.text,
  ).toHaveLength(8192)
  expect(
    parseInvestigatedResults(schema, {
      text: '😀'.repeat(4097),
      option: 'Listed',
    }).errors.text,
  ).toBe('text')
  expect(
    parseInvestigatedResults(schema, { text: 'Hello', option: 'Not listed' })
      .errors.option,
  ).toBe('option')
})
it('uses the result-reference byte limit and validates investigation references separately', () => {
  const schema = investigatedSchema(definition([], 'reference'), [])!
  expect(
    parseInvestigatedResults(schema, { reference: 'é'.repeat(100) }).result,
  ).toBeDefined()
  expect(
    parseInvestigatedResults(schema, { reference: 'é'.repeat(101) }).errors
      .reference,
  ).toBe('reference')
  expect(validInvestigationReference('é'.repeat(200))).toBe(true)
  for (const value of ['', ' ', 'a\nb', 'a\0b', 'a'.repeat(201)])
    expect(validInvestigationReference(value)).toBe(false)
})
it('ignores undeclared form properties and keeps prototype-like declared names as own JSON values', () => {
  const schema = investigatedSchema(
    definition([{ name: '__proto__', definition_id: 'text' }]),
    [field('text')],
  )!
  const raw = JSON.parse('{"__proto__":"EXPLICIT","extra":"NOT_DECLARED"}')
  const result = parseInvestigatedResults(schema, raw).result!
  expect(Object.getPrototypeOf(result)).toBeNull()
  expect(JSON.stringify(result)).toBe('{"__proto__":"EXPLICIT"}')
  expect(Object.prototype.hasOwnProperty.call(result, 'extra')).toBe(false)
})
it('rejects oversized combined selected values as a whole', () => {
  const fields = Array.from({ length: 8 }, (_, index) =>
    field('text', 'text-' + index),
  )
  const schema = investigatedSchema(
    definition(
      fields.map((value) => ({ name: value.id, definition_id: value.id })),
    ),
    fields,
  )!
  const raw = Object.fromEntries(
    fields.map((value) => [value.id, '😀'.repeat(4096)]),
  )
  expect(parseInvestigatedResults(schema, raw)).toMatchObject({
    result: undefined,
    tooLarge: true,
  })
})
