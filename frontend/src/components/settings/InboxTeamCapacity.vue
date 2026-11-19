<script setup lang="ts">
import { Users } from '@/lib/icons'
import { onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@/lib/toast'
import { accountsService } from '@/services/api'
import { inboxService, type InboxCapacity } from '@/services/inbox'

const props = defineProps<{
  team: string
  account?: string
  canWrite?: boolean
}>()
const { t } = useI18n()
const account = ref(props.account ?? '')
const accounts = ref<{ id: string; name: string }[]>([])
const rows = ref<InboxCapacity[]>([])
const busy = ref('')
const loadFailed = ref(false)
const loading = ref(false)
const enabled = ref(false)
let sequence = 0

async function load() {
  const now = ++sequence
  rows.value = []
  loadFailed.value = false
  if (!account.value || !props.team) return
  loading.value = true
  try {
    const { data } = await inboxService.teamCapacity(props.team, account.value)
    if (now === sequence) rows.value = data.data.agents
  } catch {
    if (now === sequence) loadFailed.value = true
  } finally {
    if (now === sequence) loading.value = false
  }
}
async function save(row: InboxCapacity) {
  busy.value = row.agent_id
  try {
    await inboxService.setTeamCapacity(
      props.team,
      account.value,
      row.agent_id,
      row.max_active,
    )
    toast.success(t('settings.saved'))
    await load()
  } catch {
    toast.error(t('inboxCapacity.saveError'))
  } finally {
    busy.value = ''
  }
}
watch(
  () => props.account,
  (value) => {
    if (value !== undefined) account.value = value
  },
)
watch([account, () => props.team], load)
onMounted(async () => {
  try {
    enabled.value = (await inboxService.settings()).data.data.enabled
  } catch {
    return
  }
  if (!enabled.value) return
  if (props.account === undefined) {
    try {
      accounts.value = (await accountsService.list()).data.data?.accounts ?? []
    } catch {
      loadFailed.value = true
    }
  }
  await load()
})
</script>
<template>
  <section
    v-if="enabled"
    class="inbox-ui inbox-panel"
    :aria-label="t('inboxCapacity.title')"
  >
    <div class="inbox-subsection">
      <h3 class="inbox-section-heading">{{ t('inboxCapacity.title') }}</h3>
      <p class="inbox-caption mt-1">{{ t('inboxUX.capacityDescription') }}</p>
      <label
        v-if="props.account === undefined"
        class="inbox-label mt-4 max-w-sm"
        >{{ t('inboxQueue.account')
        }}<select v-model="account">
          <option value="">{{ t('inboxUX.selectInbox') }}</option>
          <option v-for="item in accounts" :key="item.id" :value="item.id">
            {{ item.name }}
          </option>
        </select></label
      >
    </div>
    <div v-if="loadFailed" role="alert" class="inbox-subsection">
      {{ t('inboxCapacity.loadError')
      }}<button class="outline small ml-2" @click="load" type="button">
        {{ t('common.retry') }}
      </button>
    </div>
    <p v-else-if="!account" class="inbox-subsection inbox-caption">
      {{ t('inboxUX.capacityInboxHelp') }}
    </p>
    <p v-else-if="loading" role="status" class="inbox-subsection inbox-caption">
      {{ t('common.loading') }}
    </p>
    <div v-else-if="!rows.length" class="inbox-empty">
      <Users class="h-7 w-7" aria-hidden="true" />
      <h3>{{ t('inboxUX.capacityEmpty') }}</h3>
      <p>{{ t('inboxUX.capacityEmptyHelp') }}</p>
    </div>
    <div v-else class="inbox-table-wrap">
      <table class="inbox-table inbox-capacity-table">
        <thead>
          <tr>
            <th scope="col">{{ t('inboxUX.capacityAgent') }}</th>
            <th scope="col">{{ t('inboxUX.capacityActive') }}</th>
            <th scope="col">{{ t('inboxUX.capacityLimit') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.agent_id">
            <td>
              <p class="font-medium">{{ row.name }}</p>
              <span class="inbox-caption">{{
                t(
                  row.available
                    ? 'inboxCapacity.available'
                    : 'inboxCapacity.unavailable',
                )
              }}</span>
            </td>
            <td class="tabular-nums">{{ row.active }}</td>
            <td>
              <form
                v-if="canWrite"
                class="flex items-center gap-2"
                @submit.prevent="save(row)"
              >
                <label
                  ><span class="sr-only">{{
                    t('inboxCapacity.limit', { name: row.name })
                  }}</span
                  ><input
                    v-model.number="row.max_active"
                    class="inbox-control w-20"
                    type="number"
                    min="0"
                    max="10000"
                    required
                    :disabled="!!busy" /></label
                ><button type="submit" :disabled="!!busy" class="outline small">
                  {{ t('common.save') }}
                </button>
              </form>
              <span v-else>{{
                row.max_active || t('inboxCapacity.unlimited')
              }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-if="account && rows.length" class="inbox-subsection inbox-caption">
      {{ t('inboxUX.capacityLimitHelp') }}
    </p>
  </section>
</template>
