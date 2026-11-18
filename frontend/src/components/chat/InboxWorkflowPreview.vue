<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { InboxWorkflowPreview } from '@/services/inbox'
defineProps<{ preview: InboxWorkflowPreview }>()
const { t } = useI18n()
</script>
<template>
  <div class="grid gap-2 rounded-lg border border-border p-3">
    <p>{{ t(preview.matches ? 'inboxRules.matches' : 'inboxRules.noMatch') }}</p>
    <ul class="list-inside list-disc text-sm">
      <li v-for="(action, index) in preview.actions" :key="index">
        {{ t('inboxRules.action.' + action.type) }} <span v-if="!['send_text', 'send_template'].includes(action.type)"> · {{ action.value }}</span>
      </li>
    </ul>
    <p v-if="preview.send_unavailable" role="alert">{{ t('inboxRules.ineligible') }}</p>
    <div v-if="preview.reply" class="grid gap-1 border-t border-border pt-2 text-sm">
      <p><strong>{{ t('inboxRules.recipient') }}</strong> {{ preview.recipient.name }} {{ preview.recipient.phone }} · {{ preview.account }}</p>
      <p v-if="preview.reply.template_key">{{ preview.reply.template || preview.reply.template_key }} · {{ preview.reply.language }}</p>
      <p v-if="preview.reply.header" class="whitespace-pre-wrap">{{ preview.reply.header }}</p>
      <p class="whitespace-pre-wrap">{{ preview.reply.text }}</p>
      <p v-if="preview.reply.footer" class="whitespace-pre-wrap">{{ preview.reply.footer }}</p>
    </div>
  </div>
</template>
