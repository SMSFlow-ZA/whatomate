<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
const model = defineModel<string | undefined>()
const props = defineProps<{
  users: { id: string; full_name: string }[]
  label: string
  emptyLabel: string
  disabled?: boolean
}>()
const { t } = useI18n()
const search = ref('')
const visible = computed(() =>
  props.users.filter(
    (user) =>
      user.id === model.value ||
      user.full_name
        .toLocaleLowerCase()
        .includes(search.value.toLocaleLowerCase()),
  ),
)
</script>
<template>
  <div class="grid min-w-0 gap-2">
    <label class="inbox-label"
      >{{ t('inboxReview.searchAgents')
      }}<input v-model="search" type="search" :disabled="disabled"
    /></label>
    <label class="inbox-label"
      >{{ label
      }}<select v-model="model" :disabled="disabled">
        <option :value="undefined">{{ emptyLabel }}</option>
        <option v-for="user in visible" :key="user.id" :value="user.id">
          {{ user.full_name }}
        </option>
      </select></label
    >
    <p v-if="!visible.length && search" class="inbox-caption" role="status">
      {{ t('chat.noUsersFound') }}
    </p>
  </div>
</template>
