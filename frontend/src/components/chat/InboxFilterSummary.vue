<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  inboxService,
  type InboxFilters,
  type FieldDefinition,
} from '@/services/inbox'
const props = defineProps<{
  filters: InboxFilters
  accounts?: { id: string; name: string }[]
  teams?: { id: string; name: string }[]
  users?: { id: string; full_name: string }[]
}>()
const { t } = useI18n()
const definitions = ref<FieldDefinition[]>([])
let generation = 0
watch(
  () =>
    [
      ...new Set(
        [
          ...(props.filters.fields ?? []).map((field) => field.definition_id),
          ...(props.filters.predicates ?? []).map(
            (predicate) => predicate.definition_id,
          ),
        ].filter(Boolean),
      ),
    ]
      .sort()
      .join(','),
  async (ids) => {
    const request = ++generation
    definitions.value = []
    if (!ids) return
    try {
      const reply = await inboxService.fieldDefinitions()
      if (request === generation)
        definitions.value = reply.data.data.definitions
    } catch {
      /* The editor exposes lookup recovery; never print opaque IDs here. */
    }
  },
  { immediate: true },
)
onUnmounted(() => generation++)
const fieldName = (id?: string) =>
  definitions.value.find((d) => d.id === id)?.label ||
  t('inboxReview.customField')
const valueText = (value: unknown) =>
  typeof value === 'boolean'
    ? t(value ? 'common.yes' : 'common.no')
    : String(value ?? '')
const summary = computed(() => {
  const f = props.filters,
    parts: string[] = []
  if (f.queue) parts.push(t('inboxQueue.queue.' + f.queue))
  if (f.account_id)
    parts.push(
      t('inboxQueue.account') +
        ': ' +
        (props.accounts?.find((a) => a.id === f.account_id)?.name ||
          t('inboxReview.unavailableScope')),
    )
  if (f.team_id)
    parts.push(
      t('inboxQueue.team') +
        ': ' +
        (props.teams?.find((a) => a.id === f.team_id)?.name ||
          t('inboxReview.unavailableScope')),
    )
  if (f.assignee_id)
    parts.push(
      t('inboxQueue.assignee') +
        ': ' +
        (props.users?.find((a) => a.id === f.assignee_id)?.full_name ||
          t('inboxReview.unavailableScope')),
    )
  if (f.status) parts.push(t('inboxWorkflow.status.' + f.status))
  if (f.priority) parts.push(t('inboxWorkflow.priority.' + f.priority))
  for (const tag of f.tags ?? []) parts.push(t('contacts.tags') + ': ' + tag)
  for (const field of f.fields ?? [])
    parts.push(
      fieldName(field.definition_id) +
        ' ' +
        t('inboxQuality.operator.eq') +
        ' ' +
        valueText(field.value),
    )
  if (f.message_contains)
    parts.push(t('inboxReview.triggerText') + ': ' + f.message_contains)
  const predicates = (f.predicates ?? []).map((p) => {
    const label =
      p.field === 'custom_field'
        ? fieldName(p.definition_id)
        : t(
            p.field === 'tag'
              ? 'contacts.tags'
              : 'inboxWorkflow.case' +
                  (p.field === 'status' ? 'Status' : 'Priority'),
          )
    const value = ['is_empty', 'not_empty'].includes(p.operator)
      ? ''
      : ['status', 'priority'].includes(p.field)
        ? t('inboxWorkflow.' + p.field + '.' + p.value)
        : valueText(p.value)
    return `${label} ${t('inboxQuality.operator.' + p.operator)} ${value}`.trim()
  })
  if (predicates.length)
    parts.push(
      '(' +
        predicates.join(
          ' ' +
            t(f.match === 'any' ? 'inboxReview.or' : 'inboxReview.and') +
            ' ',
        ) +
        ')',
    )
  return parts.length
    ? parts.join(' ' + t('inboxReview.and') + ' ')
    : t('inboxReview.allAccessibleCases')
})
</script>
<template>
  <p class="inbox-caption break-words" data-testid="inbox-filter-summary">
    {{ summary }}
  </p>
</template>
