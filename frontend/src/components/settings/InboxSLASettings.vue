<script setup lang="ts">
import '@/assets/inbox.css'
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from '@/components/ui/alert-dialog'
import InboxTabs from '@/components/shared/InboxTabs.vue'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import InboxResponseTargetsForm from './InboxResponseTargetsForm.vue'
import InboxResponsePolicies from './InboxResponsePolicies.vue'
import InboxInternalNotifications from './InboxInternalNotifications.vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useAuthStore } from '@/stores/auth'
import { accountsService } from '@/services/api'
import { inboxService, type InboxSLAPolicy } from '@/services/inbox'
import {
  defaultInboxTargets,
  copyInboxTargets,
  inboxCalendarIssues,
} from '@/lib/inboxCalendar'

const { t } = useI18n()
const auth = useAuthStore(),
  viewer = useInboxViewerContext()
const canRead = computed(() => auth.hasPermission('settings.general', 'read'))
const canWrite = computed(() => auth.hasPermission('settings.general', 'write'))
const accounts = ref<{ id: string; name: string }[]>([]),
  policies = ref<InboxSLAPolicy[]>([])
const selected = ref(''),
  mode = ref('default'),
  policy = ref(defaultInboxTargets(''))
const baseline = ref(''),
  loading = ref(false),
  failed = ref(false),
  busy = ref(false),
  childBusy = ref(false),
  removeOpen = ref(false)
const unconfirmed = ref(false),
  accessLost = ref(false),
  error = ref('')
const child = ref<{ confirmLeave: (action: () => void) => void }>()
let loadGeneration = 0,
  mutationGeneration = 0
const existing = computed(() =>
  policies.value.some((item) => item.account_id === selected.value),
)
const locked = computed(() => busy.value || childBusy.value)
const dirty = () =>
  mode.value === 'default' &&
  !!selected.value &&
  JSON.stringify(policy.value) !== baseline.value
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(dirty)
function resetDraft() {
  policy.value = copyInboxTargets(
    policies.value.find((item) => item.account_id === selected.value) ??
      defaultInboxTargets(selected.value),
  )
  baseline.value = JSON.stringify(policy.value)
  error.value = unconfirmed.value ? t('inboxPolicy.unconfirmed') : ''
}
function confirmLeave(action: () => void) {
  if (locked.value) return
  if (mode.value !== 'default' && child.value) child.value.confirmLeave(action)
  else
    requestDiscard(() => {
      baseline.value = JSON.stringify(policy.value)
      action()
    })
}
defineExpose({ confirmLeave })
function selectAccount(event: Event) {
  const control = event.target as HTMLSelectElement,
    next = control.value
  control.value = selected.value
  confirmLeave(() => {
    selected.value = next
    if (unconfirmed.value) void load()
    else resetDraft()
  })
}
function selectMode(next: string) {
  if (mode.value === next) return
  confirmLeave(() => {
    resetDraft()
    mode.value = next
  })
}
async function load() {
  const generation = ++loadGeneration,
    context = viewer.value
  loading.value = true
  failed.value = false
  try {
    const [wa, response] = await Promise.all([
      accountsService.list(),
      inboxService.slaPolicies(),
    ])
    if (generation !== loadGeneration || context !== viewer.value) return
    accounts.value = wa.data.data?.accounts ?? []
    policies.value = response.data.data.policies
    if (!accounts.value.some((item) => item.id === selected.value))
      selected.value = ''
    unconfirmed.value = false
    resetDraft()
  } catch {
    if (generation === loadGeneration && context === viewer.value) {
      failed.value = true
      accounts.value = []
      policies.value = []
      selected.value = ''
    }
  } finally {
    if (generation === loadGeneration && context === viewer.value)
      loading.value = false
  }
}
async function save(remove = false) {
  if (
    !canWrite.value ||
    !selected.value ||
    locked.value ||
    unconfirmed.value ||
    accessLost.value ||
    (!remove && inboxCalendarIssues(policy.value).length)
  )
    return
  const account = selected.value,
    context = viewer.value,
    generation = ++mutationGeneration
  const current = () =>
    generation === mutationGeneration &&
    context === viewer.value &&
    account === selected.value
  busy.value = true
  error.value = ''
  try {
    if (remove) {
      await inboxService.deleteSlaPolicy(account)
      if (!current()) return
      policies.value = policies.value.filter(
        (item) => item.account_id !== account,
      )
    } else {
      const response = await inboxService.setSlaPolicy(
        account,
        copyInboxTargets(policy.value),
      )
      if (!current()) return
      const saved = response.data.data?.policy as InboxSLAPolicy | undefined
      if (!saved || saved.account_id !== account)
        throw new Error('Unconfirmed policy response')
      policies.value = [
        ...policies.value.filter((item) => item.account_id !== account),
        saved,
      ]
    }
    resetDraft()
    removeOpen.value = false
    toast.success(t('settings.saved'))
  } catch (cause) {
    if (!current()) return
    const status = (cause as { response?: { status?: number } })?.response
      ?.status
    if (status === 403 || status === 401) {
      accessLost.value = true
      selected.value = ''
      policies.value = []
      accounts.value = []
      error.value = t('inboxPolicy.accessChanged')
    } else {
      unconfirmed.value = !status || status >= 500
      error.value = t(
        unconfirmed.value ? 'inboxPolicy.unconfirmed' : 'inboxSla.saveError',
      )
    }
  } finally {
    if (generation === mutationGeneration && context === viewer.value)
      busy.value = false
  }
}
watch(
  [viewer, canRead],
  () => {
    loadGeneration++
    mutationGeneration++
    cancelDiscard()
    accounts.value = []
    policies.value = []
    selected.value = ''
    mode.value = 'default'
    busy.value = false
    childBusy.value = false
    accessLost.value = false
    unconfirmed.value = false
    removeOpen.value = false
    policy.value = defaultInboxTargets('')
    baseline.value = JSON.stringify(policy.value)
    error.value = ''
    if (canRead.value) void load()
  },
  { immediate: true, flush: 'sync' },
)
onUnmounted(() => {
  loadGeneration++
  mutationGeneration++
})
</script>

<template>
  <section class="inbox-ui" :aria-busy="loading || locked">
    <UnsavedChangesDialog
      :open="discardOpen"
      @leave="confirmDiscard"
      @stay="cancelDiscard"
    />
    <div class="mb-5">
      <h2 class="inbox-section-heading">{{ t('inboxSla.title') }}</h2>
      <p class="inbox-caption mt-1">{{ t('inboxUX.slaSetupHelp') }}</p>
    </div>
    <p v-if="!canRead || accessLost" class="inbox-notice" role="alert">
      {{ t('inboxPolicy.accessChanged') }}
    </p>
    <p v-else-if="loading" role="status">{{ t('common.loading') }}</p>
    <div v-else-if="failed" role="alert" class="inbox-notice">
      {{ t('inboxSla.loadError')
      }}<Button variant="outline" @click="load">{{ t('common.retry') }}</Button>
    </div>
    <template v-else>
      <fieldset class="min-w-0 border-0 p-0" :disabled="locked">
        <label class="inbox-label max-w-md"
          >{{ t('inboxQueue.account')
          }}<select :value="selected" @change="selectAccount">
            <option value="">{{ t('inboxUX.selectInbox') }}</option>
            <option
              v-for="account in accounts"
              :key="account.id"
              :value="account.id"
            >
              {{ account.name }}
            </option>
          </select></label
        >
        <p v-if="mode !== 'notifications'" class="inbox-caption mt-3">
          {{ t('inboxUX.slaPolicyHelp') }}
        </p>
        <p
          v-if="selected && mode !== 'notifications'"
          class="inbox-notice mt-3"
        >
          {{ t('inboxQuality.policyFutureOnly') }}
        </p>
        <InboxTabs
          v-if="selected"
          class="mt-4"
          :model-value="mode"
          :items="[
            { value: 'default', label: t('inboxPolicy.default') },
            { value: 'conditional', label: t('inboxPolicy.title') },
            { value: 'notifications', label: t('inboxNotice.settingsTitle') },
          ]"
          :label="t('inboxPolicy.navigation')"
          @update:model-value="selectMode"
        />
      </fieldset>
      <template v-if="selected">
        <form
          v-if="mode === 'default'"
          class="inbox-form"
          @submit.prevent="save()"
        >
          <p class="inbox-caption mt-4">{{ t('inboxPolicy.defaultHelp') }}</p>
          <p v-if="!canWrite" class="inbox-notice">
            {{ t('inboxPolicy.readOnly') }}
          </p>
          <InboxResponseTargetsForm
            v-model="policy"
            :disabled="locked || !canWrite || unconfirmed"
          />
          <p v-if="error" class="inbox-notice text-destructive" role="alert">
            {{ error }}
          </p>
          <Button
            v-if="unconfirmed"
            type="button"
            variant="outline"
            @click="requestDiscard(() => load())"
            >{{ t('inboxPolicy.refreshSaved') }}</Button
          >
          <footer v-if="canWrite" class="inbox-form-footer">
            <Button
              type="button"
              variant="outline"
              :disabled="locked"
              @click="requestDiscard(resetDraft)"
              >{{ t('common.cancel') }}</Button
            >
            <Button
              type="submit"
              :disabled="
                locked || unconfirmed || !!inboxCalendarIssues(policy).length
              "
              >{{ busy ? t('inboxPolicy.saving') : t('common.save') }}</Button
            >
            <Button
              v-if="existing"
              type="button"
              variant="outline"
              :disabled="locked || unconfirmed"
              @click="removeOpen = true"
              >{{ t('inboxUX.removeTargets') }}</Button
            >
          </footer>
        </form>
        <InboxResponsePolicies
          v-else-if="mode === 'conditional'"
          :key="selected + viewer"
          ref="child"
          :account="selected"
          :default-targets="
            policies.find((item) => item.account_id === selected)
          "
          @busy="childBusy = $event"
        />
        <InboxInternalNotifications
          v-else-if="mode === 'notifications'"
          :key="selected + viewer"
          ref="child"
          :account="selected"
          @busy="childBusy = $event"
        />
      </template>
    </template>
    <AlertDialog v-model:open="removeOpen"
      ><AlertDialogContent
        ><AlertDialogHeader
          ><AlertDialogTitle>{{ t('inboxUX.removeTargets') }}</AlertDialogTitle
          ><AlertDialogDescription>{{
            t('inboxPolicy.removeDefaultHelp')
          }}</AlertDialogDescription></AlertDialogHeader
        ><AlertDialogFooter
          ><AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel
          ><AlertDialogAction :disabled="busy" @click="save(true)">{{
            t('inboxUX.removeTargets')
          }}</AlertDialogAction></AlertDialogFooter
        ></AlertDialogContent
      ></AlertDialog
    >
  </section>
</template>
