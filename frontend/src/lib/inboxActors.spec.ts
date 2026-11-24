import { expect, it } from 'vitest'
import { inboxActorName } from './inboxActors'

it('uses the current profile without directory access', () => {
  expect(
    inboxActorName('a', { id: 'a', full_name: ' Current operator ' }),
  ).toBe('Current operator')
})
it('prefers the current own name over an older directory entry', () => {
  expect(
    inboxActorName('a', { id: 'a', full_name: 'Current' }, [
      { id: 'a', full_name: 'Earlier' },
    ]),
  ).toBe('Current')
})
it('uses only names from an already authorised supplied directory', () => {
  expect(
    inboxActorName('b', { id: 'a', full_name: 'Operator' }, [
      { id: 'b', full_name: 'Colleague' },
    ]),
  ).toBe('Colleague')
})
it('does not infer deletion or reuse another identity when the name is unavailable', () => {
  expect(
    inboxActorName('b', { id: 'a', full_name: 'Operator' }),
  ).toBeUndefined()
  expect(
    inboxActorName('b', undefined, [{ id: 'b', full_name: '   ' }]),
  ).toBeUndefined()
})
it('does not match a missing identity to a missing profile identifier', () => {
  expect(inboxActorName('', { id: '', full_name: 'Unrelated' })).toBeUndefined()
})
it('leaves literal names for the component to render as escaped text', () => {
  expect(
    inboxActorName('a', { id: 'a', full_name: '<img src=x onerror=alert(1)>' }),
  ).toBe('<img src=x onerror=alert(1)>')
})
