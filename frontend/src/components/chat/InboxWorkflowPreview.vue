<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { CheckCircle2, AlertCircle } from 'lucide-vue-next'
import type { InboxAction, InboxWorkflowPreview } from '@/services/inbox'
const props = defineProps<{
  purpose?: 'workflow' | 'reply'
  preview: InboxWorkflowPreview
  teams?: { id: string; name: string }[]
  users?: { id: string; full_name: string }[]
}>()
const { t } = useI18n()
function actionValue(action: InboxAction, index: number) {
  if (
    ['assign', 'assign_team'].includes(action.type) &&
    props.preview.action_outcomes?.[index]?.detail
  )
    return props.preview.action_outcomes[index]!.detail
  if (action.type === 'assign')
    return (
      props.users?.find((u) => u.id === action.value)?.full_name ??
      t('inboxUX.agentUnavailable')
    )
  if (action.type === 'assign_team')
    return (
      props.teams?.find((team) => team.id === action.value)?.name ??
      t('inboxQuality.teamUnavailable')
    )
  if (action.type === 'snooze')
    return t('inboxQuality.snoozeDuration', {
      minutes: action.duration_minutes,
    })
  if (action.type === 'priority' || action.type === 'status')
    return t('inboxWorkflow.' + action.type + '.' + action.value)
  return action.value
}
</script>
<template>
  <div class="inbox-ui grid gap-4">
    <template v-if="purpose !== 'reply'">
      <p class="flex items-center gap-2 text-sm font-medium">
        <CheckCircle2
          v-if="preview.matches"
          class="h-4 w-4"
          aria-hidden="true"
        /><AlertCircle v-else class="h-4 w-4" aria-hidden="true" />{{
          t(preview.matches ? 'inboxRules.matches' : 'inboxRules.noMatch')
        }}
      </p>
      <p class="inbox-caption">{{ t('inboxQuality.preflightHelp') }}</p>
      <ul v-if="preview.condition_outcomes?.length" class="grid gap-2 text-sm">
        <li
          v-for="(condition, index) in preview.condition_outcomes"
          :key="index"
        >
          <span class="font-medium">{{
            condition.label ||
            t('inboxQuality.conditionName.' + condition.field)
          }}</span>
          ·
          {{
            t(
              condition.matched
                ? 'inboxQuality.conditionMet'
                : 'inboxQuality.conditionNotMet',
            )
          }}
          <span class="inbox-caption"
            >({{
              t(
                'inboxQuality.match' +
                  (condition.group === 'any' ? 'Any' : 'All'),
              )
            }})</span
          >
        </li>
      </ul>
      <ol class="inbox-list">
        <li
          v-for="(action, index) in preview.actions"
          :key="index"
          class="flex gap-3 py-2"
        >
          <span class="inbox-action-number" aria-hidden="true">{{
            index + 1
          }}</span>
          <div class="min-w-0">
            <p class="text-sm font-medium">
              {{ t('inboxRules.action.' + action.type) }}
            </p>
            <p
              v-if="!['send_text', 'send_template'].includes(action.type)"
              class="inbox-caption mt-1 whitespace-pre-wrap break-words"
            >
              {{ actionValue(action, index) }}
            </p>
            <p
              v-if="preview.action_outcomes?.[index]"
              class="inbox-caption mt-1"
              :class="{
                'text-destructive':
                  preview.action_outcomes[index]!.status === 'blocked',
              }"
            >
              {{
                t(
                  'inboxQuality.preflight.' +
                    preview.action_outcomes[index]!.reason,
                )
              }}
            </p>
          </div>
        </li>
      </ol>
    </template>
    <p v-if="preview.send_unavailable" role="alert" class="inbox-notice">
      {{
        t(
          'inboxQuality.sendReason.' +
            (preview.send_unavailable_reason || 'unavailable'),
        )
      }}
    </p>
    <ul
      v-if="preview.reply?.missing_variables?.length"
      class="grid gap-1 text-sm"
      aria-live="polite"
    >
      <li
        v-for="variable in preview.reply.missing_variables"
        :key="variable.key"
        class="break-words"
      >
        {{ t('inboxPersonalisation.missing', { key: variable.key }) }}
        {{ t('inboxPersonalisation.reason.' + variable.reason) }}
      </li>
    </ul>
    <div class="border-t border-border pt-4 text-sm">
      <p class="inbox-caption">{{ t('inboxRules.recipient') }}</p>
      <p class="mt-1 font-medium">{{ preview.recipient.name }}</p>
      <p class="inbox-caption break-words">
        {{ preview.recipient.phone }} / {{ preview.account }}
      </p>
      <p v-if="preview.reply?.template_key" class="inbox-caption mt-2">
        {{ preview.reply.template || preview.reply.template_key }} ({{
          preview.reply.language
        }})
      </p>
      <p v-if="preview.reply?.canned_name" class="mt-2 font-medium">
        {{ preview.reply.canned_name }}
      </p>
      <div
        v-if="preview.reply"
        class="mt-3 grid gap-1 rounded-lg bg-muted/60 p-3"
      >
        <p
          v-if="preview.reply.header"
          class="whitespace-pre-wrap break-words font-medium"
        >
          {{ preview.reply.header }}
        </p>
        <p class="whitespace-pre-wrap break-words">{{ preview.reply.text }}</p>
        <p
          v-if="preview.reply.footer"
          class="inbox-caption whitespace-pre-wrap"
        >
          {{ preview.reply.footer }}
        </p>
      </div>
      <div v-if="preview.reply?.interactive" class="mt-2 grid gap-2 text-sm">
        <p class="inbox-caption">
          {{ t('inboxPersonalisation.buttonPreview') }}
        </p>
        <ul v-if="preview.reply.interactive.buttons?.length" class="grid gap-1">
          <li
            v-for="button in preview.reply.interactive.buttons"
            :key="button.id"
            class="rounded-md border border-border px-3 py-2 break-words"
          >
            {{ button.title }}
          </li>
        </ul>
        <p
          v-if="
            preview.reply.interactive.button_text ||
            preview.reply.interactive.display_text
          "
          class="rounded-md border border-border px-3 py-2 break-words"
        >
          {{
            preview.reply.interactive.button_text ||
            preview.reply.interactive.display_text
          }}
        </p>
        <p v-if="preview.reply.interactive.url" class="inbox-caption break-all">
          {{ preview.reply.interactive.url }}
        </p>
        <p
          v-if="preview.reply.interactive.type === 'flow'"
          class="inbox-caption"
        >
          {{ t('inboxPersonalisation.flowPreview') }}
        </p>
        <p
          v-if="preview.reply.interactive.type === 'voice_call'"
          class="inbox-caption"
        >
          {{
            t('inboxPersonalisation.callPreview', {
              minutes: preview.reply.interactive.ttl_minutes || 15,
            })
          }}
        </p>
      </div>
    </div>
  </div>
</template>
