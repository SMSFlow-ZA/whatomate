<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@/lib/toast'
import { accountsService } from '@/services/api'
import { inboxService, type InboxCapacity } from '@/services/inbox'

const props = defineProps<{ team: string; account?: string; canWrite?: boolean }>()
const { t } = useI18n()
const account = ref(props.account ?? '')
const accounts = ref<{ id: string; name: string }[]>([])
const rows = ref<InboxCapacity[]>([])
const busy = ref('')
const loadFailed = ref(false)
const enabled = ref(false)
let sequence = 0

async function load() {
  const now = ++sequence
  rows.value = []
  loadFailed.value = false
  if (!account.value || !props.team) return
  try {
    const { data } = await inboxService.teamCapacity(props.team, account.value)
    if (now === sequence) rows.value = data.data.agents
  } catch { if (now === sequence) loadFailed.value = true }
}
async function save(row: InboxCapacity) {
  busy.value = row.agent_id
  try {
    await inboxService.setTeamCapacity(props.team, account.value, row.agent_id, row.max_active)
    toast.success(t('settings.saved'))
    await load()
  } catch { toast.error(t('inboxCapacity.saveError')) }
  finally { busy.value = '' }
}
watch(() => props.account, value => { if (value !== undefined) account.value = value })
watch([account, () => props.team], load)
onMounted(async () => {
  try { enabled.value = (await inboxService.settings()).data.data.enabled } catch { return }
  if (!enabled.value) return
  if (props.account === undefined) {
    try { accounts.value = (await accountsService.list()).data.data?.accounts ?? [] } catch { loadFailed.value = true }
  }
  await load()
})
</script>
<template>
  <section v-if="enabled" class="grid gap-2 rounded-lg border border-border p-3">
    <h3 class="font-medium">{{ t('inboxCapacity.title') }}</h3>
    <label v-if="props.account === undefined">
      {{ t('inboxQueue.account') }}
      <select v-model="account" class="rounded-md border border-input bg-background p-1"><option value="">—</option><option v-for="item in accounts" :key="item.id" :value="item.id">{{ item.name }}</option></select>
    </label>
    <p v-if="props.canWrite && account" class="text-sm text-muted-foreground">{{ t('inboxCapacity.help') }}</p>
    <p v-if="loadFailed" role="alert">{{ t('inboxCapacity.loadError') }}</p>
    <ul v-else class="divide-y divide-border">
      <li v-for="row in rows" :key="row.agent_id">
        <form class="flex flex-wrap items-center gap-2 py-2" @submit.prevent="save(row)">
          <span class="mr-auto">{{ row.name }} · {{ t(row.available ? 'inboxCapacity.available' : 'inboxCapacity.unavailable') }}</span>
          <span>{{ t('inboxCapacity.workload', { count: row.active }) }}</span>
          <label v-if="canWrite">
            <span class="sr-only">{{ t('inboxCapacity.limit', { name: row.name }) }}</span>
            <input v-model.number="row.max_active" class="w-24 rounded-md border border-input bg-background p-1" type="number" min="0" max="10000" required :disabled="!!busy" />
          </label>
          <span v-else>{{ row.max_active || t('inboxCapacity.unlimited') }}</span>
          <button v-if="canWrite" class="outline" type="submit" :disabled="!!busy">{{ t('common.save') }}</button>
        </form>
      </li>
    </ul>
  </section>
</template>
