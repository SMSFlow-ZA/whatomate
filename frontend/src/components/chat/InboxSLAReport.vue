<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTeamsStore } from '@/stores/teams'
import { accountsService } from '@/services/api'
import { inboxService, type QueueCase } from '@/services/inbox'
import InboxSLAIndicators from './InboxSLAIndicators.vue'

const { t } = useI18n()
const teamsStore = useTeamsStore()
const rows = ref<QueueCase[]>([])
const account = ref('')
const team = ref('')
const accounts = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string }[]>([])
const offset = ref(0)
const hasMore = ref(false)
const available = ref(false)
const failed = ref(false)
const all = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
let sequence = 0
const visible = computed(() => rows.value.filter(row => all.value || Object.values(row.sla ?? {}).some(target => ['at_risk', 'breached'].includes(target.status))))
async function load() {
  const request = ++sequence
  try {
    const { data } = await inboxService.slaReport(account.value, team.value, offset.value)
    if (request !== sequence) return
    rows.value = data.data.cases ?? []
    hasMore.value = data.data.has_more
    failed.value = false
  } catch { if (request === sequence) failed.value = true }
}
watch([team, account], () => { offset.value = 0; void load() })
watch(offset, load)
onMounted(async () => {
  try { available.value = (await inboxService.settings()).data.data.enabled } catch { return }
  if (!available.value) return
  void accountsService.list().then(reply => { accounts.value = reply.data.data?.accounts ?? [] }).catch(() => {})
  void teamsStore.fetchTeams().then(result => { teams.value = result.teams }).catch(() => {})
  await load()
  timer = setInterval(load, 30000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section v-if="available" class="mb-4 rounded-lg border border-border p-4">
    <h2 class="mb-3 font-semibold">{{ t('inboxSla.report') }}</h2>
    <div class="flex flex-wrap items-center gap-3">
      <label>{{ t('inboxQueue.account') }} <select v-model="account" class="rounded border border-input bg-background p-1"><option value="">{{ t('inboxSla.any') }}</option><option v-for="entry in accounts" :key="entry.id" :value="entry.id">{{ entry.name }}</option></select></label>
      <label>{{ t('inboxQueue.team') }} <select v-model="team" class="rounded border border-input bg-background p-1"><option value="">{{ t('inboxSla.any') }}</option><option v-for="entry in teams" :key="entry.id" :value="entry.id">{{ entry.name }}</option></select></label>
      <label><input v-model="all" type="checkbox" /> {{ t('inboxSla.allCases') }}</label>
    </div>
    <p v-if="failed" class="mt-3" role="alert">{{ t('inboxSla.loadError') }}</p>
    <p v-else-if="!visible.length" class="mt-3 text-sm text-muted-foreground">{{ t('inboxSla.empty') }}</p>
    <ul v-else class="mt-3 divide-y divide-border">
      <li v-for="row in visible" :key="row.id" class="flex flex-wrap items-center gap-2 py-2">
        <RouterLink class="underline" :to="{ name: 'chat-conversation', params: { contactId: row.contact_id }, query: { inbox_account: row.account_name, inbox_account_id: row.account_id } }">{{ row.contact_name || t('inboxQueue.unnamed') }} · {{ row.account_name }}</RouterLink>
        <InboxSLAIndicators v-if="row.sla" :sla="row.sla" />
      </li>
    </ul>
    <div v-if="offset || hasMore" class="mt-3 flex gap-2">
      <button :disabled="!offset" class="outline" @click="offset -= 50">{{ t('inboxQueue.previous') }}</button>
      <button :disabled="!hasMore" class="outline" @click="offset += 50">{{ t('inboxQueue.next') }}</button>
    </div>
  </section>
</template>
