<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { CheckCircle, AlertCircle } from '@/lib/icons'
import type { InboxAction, InboxWorkflowPreview } from '@/services/inbox'
const props = defineProps<{
  preview: InboxWorkflowPreview
  users?: { id: string; full_name: string }[]
}>()
const { t } = useI18n()
function actionValue(action: InboxAction) {
  if (action.type === 'assign')
    return (
      props.users?.find((u) => u.id === action.value)?.full_name ??
      t('inboxUX.agentUnavailable')
    )
  if (action.type === 'priority' || action.type === 'status')
    return t('inboxWorkflow.' + action.type + '.' + action.value)
  return action.value
}
</script>
<template>
  <div class="inbox-ui grid gap-4">
    <p class="flex items-center gap-2 text-sm font-medium">
      <CheckCircle
        v-if="preview.matches"
        class="h-4 w-4"
        aria-hidden="true"
      /><AlertCircle v-else class="h-4 w-4" aria-hidden="true" />{{
        t(preview.matches ? 'inboxRules.matches' : 'inboxRules.noMatch')
      }}
    </p>
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
            {{ actionValue(action) }}
          </p>
        </div>
      </li>
    </ol>
    <p v-if="preview.send_unavailable" role="alert" class="inbox-notice">
      {{ t('inboxRules.ineligible') }}
    </p>
    <div v-if="preview.reply" class="border-t border-border pt-4 text-sm">
      <p class="inbox-caption">{{ t('inboxRules.recipient') }}</p>
      <p class="mt-1 font-medium">{{ preview.recipient.name }}</p>
      <p class="inbox-caption">
        {{ preview.recipient.phone }} / {{ preview.account }}
      </p>
      <p v-if="preview.reply.template_key" class="inbox-caption mt-2">
        {{ preview.reply.template || preview.reply.template_key }} ({{
          preview.reply.language
        }})
      </p>
      <div class="mt-3 grid gap-1 rounded-lg inbox-preview-surface p-3">
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
    </div>
  </div>
</template>
