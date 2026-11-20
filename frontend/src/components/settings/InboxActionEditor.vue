<script setup lang="ts">
import InboxAgentSelect from '@/components/shared/InboxAgentSelect.vue'
import { useI18n } from 'vue-i18n'
import type { InboxAction } from '@/services/inbox'
const model = defineModel<InboxAction>({ required: true })
defineProps<{
  teams?: { id: string; name: string }[]
  users: { id: string; full_name: string }[]
  tags: { name: string }[]
  templates: { id: string; name: string; language: string }[]
}>()
const { t } = useI18n()
const types: InboxAction['type'][] = [
  'assign',
  'assign_team',
  'snooze',
  'tag',
  'priority',
  'status',
  'note',
  'send_text',
  'send_template',
]
function changeType() {
  model.value.value = ''
  model.value.consent_confirmed = false
  model.value.duration_minutes = undefined
}
</script>
<template>
  <div class="grid min-w-0 gap-3">
    <label class="inbox-label"
      >{{ t('inboxRules.actionType') }}
      <select v-model="model.type" @change="changeType">
        <option v-for="type in types" :key="type" :value="type">
          {{ t('inboxRules.action.' + type) }}
        </option>
      </select>
    </label>
    <label
      v-if="model.type === 'priority' || model.type === 'status'"
      class="inbox-label"
      >{{ t('inboxRules.actionValue') }}
      <select v-model="model.value">
        <option value="">{{ t('inboxUX.chooseValue') }}</option>
        <option
          v-for="value in model.type === 'priority'
            ? ['low', 'normal', 'high', 'urgent']
            : ['open', 'pending', 'waiting_customer', 'resolved']"
          :key="value"
          :value="value"
        >
          {{ t('inboxWorkflow.' + model.type + '.' + value) }}
        </option>
      </select>
    </label>
    <label v-else-if="model.type === 'snooze'" class="inbox-label"
      >{{ t('inboxQuality.snoozeMinutes')
      }}<input
        v-model.number="model.duration_minutes"
        type="number"
        min="1"
        max="43200"
        required
      /><span class="inbox-caption">{{
        t('inboxQuality.snoozeHelp')
      }}</span></label
    >
    <label v-else-if="model.type === 'assign_team'" class="inbox-label"
      >{{ t('inboxQueue.team')
      }}<select v-model="model.value">
        <option value="">{{ t('inboxQuality.chooseTeam') }}</option>
        <option v-for="team in teams" :key="team.id" :value="team.id">
          {{ team.name }}
        </option>
      </select></label
    >
    <InboxAgentSelect
      v-else-if="model.type === 'assign'"
      :model-value="model.value"
      @update:model-value="model.value = $event ?? ''"
      :users="users"
      :label="t('inboxQueue.assignee')"
      :empty-label="t('inboxQueue.chooseAgent')"
    />
    <label v-else-if="model.type === 'tag'" class="inbox-label"
      >{{ t('contacts.tags') }}
      <select v-model="model.value">
        <option value="">{{ t('inboxQueue.chooseTag') }}</option>
        <option v-for="tag in tags" :key="tag.name" :value="tag.name">
          {{ tag.name }}
        </option>
      </select>
    </label>
    <template v-else-if="model.type === 'send_template'">
      <label class="inbox-label"
        >{{ t('inboxRules.action.send_template') }}
        <select v-model="model.value">
          <option value="">{{ t('inboxUX.chooseTemplate') }}</option>
          <option v-for="item in templates" :key="item.id" :value="item.id">
            {{ item.name }} ({{ item.language }})
          </option>
        </select>
      </label>
      <p class="inbox-caption">{{ t('inboxUX.templateHelp') }}</p>
      <label class="inbox-check"
        ><input v-model="model.consent_confirmed" type="checkbox" />
        {{ t('inboxRules.consent') }}</label
      >
    </template>
    <label v-else class="inbox-label"
      >{{ t(model.type === 'note' ? 'inboxUX.noteText' : 'inboxUX.replyText') }}
      <textarea
        v-model="model.value"
        rows="3"
        maxlength="4096"
        :aria-label="t('inboxRules.actionValue')"
      />
      <span class="inbox-caption">{{
        t(model.type === 'note' ? 'inboxUX.noteHelp' : 'inboxUX.replyHelp')
      }}</span>
    </label>
  </div>
</template>
