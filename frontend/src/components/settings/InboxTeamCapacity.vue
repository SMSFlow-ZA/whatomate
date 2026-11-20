<script setup lang="ts">
import '@/assets/inbox.css'
import { UsersRound } from 'lucide-vue-next'
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { accountsService } from '@/services/api'
import InboxCapacityPresets from './InboxCapacityPresets.vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
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
const settingsFailed = ref(false)
const accountsFailed = ref(false)
const lastUpdated = ref('')
const originalLimits = new Map<string, number>()
let timer: ReturnType<typeof setInterval> | undefined
let sequence = 0
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(
    () =>
      !!props.canWrite &&
      rows.value.some(
        (row) =>
          row.max_active !==
          (originalLimits.get(row.agent_id) ?? row.max_active),
      ),
  )
function selectAccount(event: Event) {
  const control = event.target as HTMLSelectElement
  const value = control.value
  control.value = account.value
  requestDiscard(() => {
    account.value = value
  })
}

async function load() {
  const now = ++sequence
  loadFailed.value = false
  if (!account.value || !props.team) return
  loading.value = true
  try {
    const { data } = await inboxService.teamCapacity(props.team, account.value)
    if (now === sequence) {
      const drafts = new Map(
        rows.value
          .filter(
            (row) =>
              originalLimits.has(row.agent_id) &&
              row.max_active !== originalLimits.get(row.agent_id),
          )
          .map((row) => [row.agent_id, row.max_active]),
      )
      rows.value = data.data.agents.map((row) => {
        originalLimits.set(row.agent_id, row.max_active)
        return {
          ...row,
          max_active: drafts.get(row.agent_id) ?? row.max_active,
        }
      })
      lastUpdated.value = new Date().toLocaleTimeString()
    }
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
    originalLimits.delete(row.agent_id)
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
watch([account, () => props.team], () => {
  rows.value = []
  originalLimits.clear()
  void load()
})
async function initialize() {
  settingsFailed.value = false
  accountsFailed.value = false
  try {
    enabled.value = (await inboxService.settings()).data.data.enabled
  } catch {
    settingsFailed.value = true
    return
  }
  if (!enabled.value) return
  if (props.account === undefined) {
    try {
      accounts.value = (await accountsService.list()).data.data?.accounts ?? []
      if (!account.value && accounts.value.length === 1)
        account.value = accounts.value[0].id
    } catch {
      accountsFailed.value = true
      return
    }
  }
  await load()
}
function refreshVisible() {
  if (
    document.visibilityState === 'visible' &&
    !busy.value &&
    !loading.value &&
    !settingsFailed.value &&
    !accountsFailed.value &&
    enabled.value
  )
    void load()
}
onMounted(() => {
  void initialize()
  timer = setInterval(refreshVisible, 30000)
  document.addEventListener('visibilitychange', refreshVisible)
})
onUnmounted(() => {
  sequence++
  clearInterval(timer)
  document.removeEventListener('visibilitychange', refreshVisible)
})
function presetApplied() {
  rows.value = []
  originalLimits.clear()
  void load()
}
</script>
<template>
  <UnsavedChangesDialog
    :open="discardOpen"
    @leave="confirmDiscard"
    @stay="cancelDiscard"
  />
  <div
    v-if="settingsFailed || accountsFailed"
    role="alert"
    class="inbox-ui inbox-notice"
  >
    {{
      t(
        settingsFailed
          ? 'inboxQuality.settingsLoadError'
          : 'inboxQuality.accountLoadError',
      )
    }}
    <Button size="sm" variant="outline" @click="initialize">{{
      t('common.retry')
    }}</Button>
  </div>
  <section
    v-if="enabled"
    class="inbox-ui inbox-panel"
    :aria-label="t('inboxCapacity.title')"
  >
    <div class="inbox-subsection">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h3 class="inbox-section-heading">{{ t('inboxCapacity.title') }}</h3>
        <Button
          type="button"
          size="sm"
          variant="outline"
          :disabled="loading || !!busy || !account"
          @click="load"
          >{{ t('inboxQuality.refresh') }}</Button
        >
      </div>
      <p v-if="lastUpdated" class="inbox-caption mt-1" role="status">
        {{ t('inboxQuality.lastUpdated', { time: lastUpdated }) }}
      </p>
      <p class="inbox-caption mt-1">{{ t('inboxUX.capacityDescription') }}</p>
      <label
        v-if="props.account === undefined"
        class="inbox-label mt-4 max-w-sm"
        >{{ t('inboxQueue.account')
        }}<select :value="account" :disabled="!!busy" @change="selectAccount">
          <option value="">{{ t('inboxUX.selectInbox') }}</option>
          <option v-for="item in accounts" :key="item.id" :value="item.id">
            {{ item.name }}
          </option>
        </select></label
      >
    </div>
    <div v-if="loadFailed" role="alert" class="inbox-subsection">
      {{ t('inboxCapacity.loadError')
      }}<Button class="ml-2" size="sm" variant="outline" @click="load">{{
        t('common.retry')
      }}</Button>
    </div>
    <p v-if="!account" class="inbox-subsection inbox-caption">
      {{ t('inboxUX.capacityInboxHelp') }}
    </p>
    <p
      v-else-if="loading && !rows.length"
      role="status"
      class="inbox-subsection inbox-caption"
    >
      {{ t('common.loading') }}
    </p>
    <div v-else-if="!rows.length" class="inbox-empty">
      <UsersRound class="h-7 w-7" aria-hidden="true" />
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
              <p class="font-medium">
                {{ row.name
                }}<span v-if="row.inactive" class="inbox-caption block">{{
                  t('inboxQuality.inactiveAgent')
                }}</span>
              </p>
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
                ><Button
                  size="sm"
                  variant="outline"
                  type="submit"
                  :disabled="!!busy"
                  >{{ t('common.save') }}</Button
                >
              </form>
              <span v-else>{{
                row.max_active || t('inboxCapacity.unlimited')
              }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <InboxCapacityPresets
      v-if="canWrite && account && rows.length"
      :team="team"
      :account="account"
      :agents="rows.map((row) => row.agent_id)"
      @applied="presetApplied()"
    />
    <p v-if="account && rows.length" class="inbox-subsection inbox-caption">
      {{ t('inboxUX.capacityLimitHelp') }}
    </p>
  </section>
</template>
