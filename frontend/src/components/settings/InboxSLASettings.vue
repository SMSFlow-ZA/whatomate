<script setup lang="ts">
import '@/assets/inbox.css'
import { computed, onMounted, ref, watch } from 'vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
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
import { Button } from '@/components/ui/button'
import { accountsService } from '@/services/api'
import { inboxService, type InboxSLAPolicy } from '@/services/inbox'

const { t } = useI18n()
const accounts = ref<{ id: string; name: string }[]>([])
const policies = ref<InboxSLAPolicy[]>([])
const selected = ref('')
const busy = ref(false)
const removeOpen = ref(false)
const failed = ref(false)
const weekdays = [
  'Sunday',
  'Monday',
  'Tuesday',
  'Wednesday',
  'Thursday',
  'Friday',
  'Saturday',
]
const defaults = (id: string): InboxSLAPolicy => ({
  account_id: id,
  first_reply_minutes: 30,
  next_reply_minutes: 60,
  resolution_minutes: 480,
  business_hours: false,
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  shifts: [],
})
const policy = ref(defaults(''))
const baseline = ref(''),
  saveError = ref('')
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(
    () => !!selected.value && JSON.stringify(policy.value) !== baseline.value,
  )
const timezones = (
  Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }
).supportedValuesOf?.('timeZone') ?? ['Africa/Johannesburg', 'UTC']
const existing = computed(() =>
  policies.value.some((item) => item.account_id === selected.value),
)
watch(selected, (id) => {
  const saved = policies.value.find((item) => item.account_id === id)
  policy.value = saved
    ? { ...saved, shifts: saved.shifts?.map((shift) => ({ ...shift })) ?? [] }
    : defaults(id)
  baseline.value = JSON.stringify(policy.value)
})
function selectAccount(event: Event) {
  const control = event.target as HTMLSelectElement
  const value = control.value
  control.value = selected.value
  requestDiscard(() => {
    selected.value = value
  })
}
defineExpose({ confirmLeave: requestDiscard })
async function load() {
  try {
    const [wa, rules] = await Promise.all([
      accountsService.list(),
      inboxService.slaPolicies(),
    ])
    accounts.value = wa.data.data?.accounts ?? []
    policies.value = rules.data.data.policies
    failed.value = false
  } catch {
    failed.value = true
  }
}
async function save(remove = false) {
  const id = selected.value
  busy.value = true
  try {
    if (remove) await inboxService.deleteSlaPolicy(id)
    else await inboxService.setSlaPolicy(id, policy.value)
    await load()
    if (selected.value === id) {
      policy.value =
        policies.value.find((item) => item.account_id === id) ?? defaults(id)
      baseline.value = JSON.stringify(policy.value)
    }
    saveError.value = ''
    removeOpen.value = false
    toast.success(t('settings.saved'))
  } catch {
    saveError.value = t('inboxSla.saveError')
    toast.error(saveError.value)
  } finally {
    busy.value = false
  }
}
const localTime = (value: number) =>
  [Math.floor(value / 60), value % 60]
    .map((n) => String(n).padStart(2, '0'))
    .join(':')
function minute(event: Event) {
  const [hour, min] = (event.target as HTMLInputElement).value
    .split(':')
    .map(Number)
  return hour * 60 + min
}
onMounted(load)
</script>

<template>
  <section class="inbox-ui">
    <UnsavedChangesDialog
      :open="discardOpen"
      @leave="confirmDiscard"
      @stay="cancelDiscard"
    />
    <div class="mb-5">
      <h2 class="inbox-section-heading">{{ t('inboxSla.title') }}</h2>
      <p class="inbox-caption mt-1">{{ t('inboxUX.slaSetupHelp') }}</p>
    </div>
    <div v-if="failed" role="alert" class="inbox-notice">
      {{ t('inboxSla.loadError')
      }}<Button size="sm" variant="outline" @click="load">{{
        t('common.retry')
      }}</Button>
    </div>
    <form v-else class="inbox-form" @submit.prevent="save()">
      <section class="inbox-form-section">
        <label class="inbox-label max-w-md"
          >{{ t('inboxQueue.account')
          }}<select :value="selected" :disabled="busy" @change="selectAccount">
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
        <p class="inbox-caption mt-3">{{ t('inboxUX.slaPolicyHelp') }}</p>
        <p class="inbox-notice mt-3">
          {{ t('inboxQuality.policyFutureOnly') }}
        </p>
        <p v-if="saveError" class="inbox-notice text-destructive" role="alert">
          {{ saveError }}
        </p>
      </section>
      <template v-if="selected">
        <section class="inbox-form-section">
          <h3 class="inbox-section-heading">
            {{ t('inboxUX.responseTargets') }}
          </h3>
          <p>{{ t('inboxUX.targetMinutesHelp') }}</p>
          <div class="inbox-sla-targets">
            <label class="inbox-label"
              >{{ t('inboxSla.target.first_reply')
              }}<input
                v-model.number="policy.first_reply_minutes"
                type="number"
                min="1"
                max="43200"
                required
                :aria-label="
                  t('inboxSla.target.first_reply') +
                  ' (' +
                  t('inboxSla.minutes') +
                  ')'
                " /></label
            ><label class="inbox-label"
              >{{ t('inboxSla.target.next_reply')
              }}<input
                v-model.number="policy.next_reply_minutes"
                type="number"
                min="1"
                max="43200"
                required
                :aria-label="
                  t('inboxSla.target.next_reply') +
                  ' (' +
                  t('inboxSla.minutes') +
                  ')'
                " /></label
            ><label class="inbox-label"
              >{{ t('inboxSla.target.resolution')
              }}<input
                v-model.number="policy.resolution_minutes"
                type="number"
                min="1"
                max="43200"
                required
                :aria-label="
                  t('inboxSla.target.resolution') +
                  ' (' +
                  t('inboxSla.minutes') +
                  ')'
                "
            /></label>
          </div>
        </section>
        <section class="inbox-form-section">
          <h3 class="inbox-section-heading">{{ t('inboxUX.workingClock') }}</h3>
          <p>{{ t('inboxUX.slaClockHelp') }}</p>
          <label class="inbox-check"
            ><input v-model="policy.business_hours" type="checkbox" /><span
              >{{ t('inboxSla.businessHours')
              }}<span class="inbox-caption block">{{
                t('inboxUX.elapsedHelp')
              }}</span></span
            ></label
          >
          <template v-if="policy.business_hours"
            ><label class="inbox-label mt-4 max-w-md"
              >{{ t('inboxSla.timezone')
              }}<input
                v-model.trim="policy.timezone"
                placeholder="Africa/Johannesburg"
                list="inbox-valid-timezones"
                required /></label
            ><datalist id="inbox-valid-timezones">
              <option v-for="zone in timezones" :key="zone" :value="zone" />
            </datalist>
            <div class="mt-4 grid gap-3">
              <div
                v-for="(shift, i) in policy.shifts"
                :key="i"
                class="inbox-shift-row"
              >
                <label class="inbox-label"
                  >{{ t('inboxSla.day')
                  }}<select v-model.number="shift.Day">
                    <option v-for="(day, n) in weekdays" :key="day" :value="n">
                      {{ day }}
                    </option>
                  </select></label
                ><label class="inbox-label"
                  >{{ t('inboxSla.from')
                  }}<input
                    type="time"
                    :value="localTime(shift.StartMinute)"
                    required
                    @input="shift.StartMinute = minute($event)" /></label
                ><label class="inbox-label"
                  >{{ t('inboxSla.to')
                  }}<input
                    v-if="shift.EndMinute !== 1440"
                    type="time"
                    :value="localTime(shift.EndMinute)"
                    required
                    @input="shift.EndMinute = minute($event)"
                  /><span v-else class="inbox-control">24:00</span></label
                ><label class="inbox-check self-end pb-3"
                  ><input
                    type="checkbox"
                    :checked="shift.EndMinute === 1440"
                    @change="
                      shift.EndMinute = shift.EndMinute === 1440 ? 1020 : 1440
                    "
                  />{{ t('inboxSla.midnight') }}</label
                ><Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  @click="policy.shifts.splice(i, 1)"
                  >{{ t('common.remove') }}</Button
                >
              </div>
            </div>
            <Button
              class="mt-3"
              type="button"
              variant="outline"
              size="sm"
              :disabled="policy.shifts.length >= 28"
              @click="
                policy.shifts.push({
                  Day: 1,
                  StartMinute: 480,
                  EndMinute: 1020,
                })
              "
              >{{ t('inboxSla.addShift') }}</Button
            ></template
          >
        </section>
        <footer class="inbox-form-footer">
          <Button
            type="button"
            variant="outline"
            :disabled="busy"
            @click="
              requestDiscard(() => {
                const saved = policies.find(
                  (item) => item.account_id === selected,
                )
                policy = saved
                  ? {
                      ...saved,
                      shifts: saved.shifts.map((shift) => ({ ...shift })),
                    }
                  : defaults(selected)
                baseline = JSON.stringify(policy)
              })
            "
            >{{ t('common.cancel') }}</Button
          >
          <Button type="submit" :disabled="busy">{{ t('common.save') }}</Button
          ><Button
            v-if="existing"
            type="button"
            variant="outline"
            :disabled="busy"
            @click="removeOpen = true"
            >{{ t('inboxUX.removeTargets') }}</Button
          >
        </footer>
      </template>
    </form>
    <AlertDialog v-model:open="removeOpen"
      ><AlertDialogContent
        ><AlertDialogHeader
          ><AlertDialogTitle>{{ t('inboxUX.removeTargets') }}</AlertDialogTitle
          ><AlertDialogDescription>{{
            t('inboxUX.removeTargetsHelp')
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
