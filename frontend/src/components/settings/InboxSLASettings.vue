<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@/lib/toast'
import { accountsService } from '@/services/api'
import { inboxService, type InboxSLAPolicy } from '@/services/inbox'

const { t } = useI18n()
const accounts = ref<{ id: string; name: string }[]>([])
const policies = ref<InboxSLAPolicy[]>([])
const selected = ref('')
const busy = ref(false)
const failed = ref(false)
const weekdays = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
const defaults = (id: string): InboxSLAPolicy => ({
  account_id: id, first_reply_minutes: 30, next_reply_minutes: 60, resolution_minutes: 480,
  business_hours: false, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, shifts: [],
})
const policy = ref(defaults(''))
const existing = computed(() => policies.value.some(item => item.account_id === selected.value))
watch(selected, id => {
  const saved = policies.value.find(item => item.account_id === id)
  policy.value = saved ? { ...saved, shifts: saved.shifts?.map(shift => ({ ...shift })) ?? [] } : defaults(id)
})
async function load() {
  try {
    const [wa, rules] = await Promise.all([accountsService.list(), inboxService.slaPolicies()])
    accounts.value = wa.data.data?.accounts ?? []
    policies.value = rules.data.data.policies
    failed.value = false
  } catch { failed.value = true }
}
async function save(remove = false) {
  const id = selected.value
  busy.value = true
  try {
    if (remove) await inboxService.deleteSlaPolicy(id)
    else await inboxService.setSlaPolicy(id, policy.value)
    await load()
    if (remove && selected.value === id) policy.value = defaults(id)
    toast.success(t('settings.saved'))
  } catch { toast.error(t('inboxSla.saveError')) }
  finally { busy.value = false }
}
const localTime = (value: number) => [Math.floor(value / 60), value % 60].map(n => String(n).padStart(2, '0')).join(':')
function minute(event: Event) {
  const [hour, min] = (event.target as HTMLInputElement).value.split(':').map(Number)
  return hour * 60 + min
}
onMounted(load)
</script>

<template>
  <details class="mt-4 rounded-lg border border-border p-3">
    <summary class="cursor-pointer font-medium">{{ t('inboxSla.title') }}</summary>
    <p v-if="failed" role="alert" class="mt-2">{{ t('inboxSla.loadError') }}</p>
    <form v-else class="mt-3 grid gap-3" @submit.prevent="save()">
      <label>{{ t('inboxQueue.account') }}
        <select v-model="selected"><option value="">—</option><option v-for="account in accounts" :key="account.id" :value="account.id">{{ account.name }}</option></select>
      </label>
      <template v-if="selected">
        <div class="grid gap-2 md:grid-cols-3">
          <label>{{ t('inboxSla.target.first_reply') }} ({{ t('inboxSla.minutes') }}) <input v-model.number="policy.first_reply_minutes" type="number" min="1" max="43200" required /></label>
          <label>{{ t('inboxSla.target.next_reply') }} ({{ t('inboxSla.minutes') }}) <input v-model.number="policy.next_reply_minutes" type="number" min="1" max="43200" required /></label>
          <label>{{ t('inboxSla.target.resolution') }} ({{ t('inboxSla.minutes') }}) <input v-model.number="policy.resolution_minutes" type="number" min="1" max="43200" required /></label>
        </div>
        <label><input v-model="policy.business_hours" type="checkbox" /> {{ t('inboxSla.businessHours') }}</label>
        <template v-if="policy.business_hours">
          <label>{{ t('inboxSla.timezone') }} <input v-model.trim="policy.timezone" placeholder="Africa/Johannesburg" required /></label>
          <div v-for="(shift, i) in policy.shifts" :key="i" class="flex flex-wrap items-end gap-2">
            <label>{{ t('inboxSla.day') }} <select v-model.number="shift.Day"><option v-for="(day, n) in weekdays" :key="day" :value="n">{{ day }}</option></select></label>
            <label>{{ t('inboxSla.from') }} <input type="time" :value="localTime(shift.StartMinute)" required @input="shift.StartMinute = minute($event)" /></label>
            <label>{{ t('inboxSla.to') }} <input v-if="shift.EndMinute !== 1440" type="time" :value="localTime(shift.EndMinute)" required @input="shift.EndMinute = minute($event)" /><span v-else class="block">24:00</span></label>
            <label><input type="checkbox" :checked="shift.EndMinute === 1440" @change="shift.EndMinute = shift.EndMinute === 1440 ? 1020 : 1440" /> {{ t('inboxSla.midnight') }}</label>
            <button type="button" class="outline" @click="policy.shifts.splice(i, 1)">{{ t('common.remove') }}</button>
          </div>
          <button class="justify-self-start" type="button" class="outline" :disabled="policy.shifts.length >= 28" @click="policy.shifts.push({ Day: 1, StartMinute: 480, EndMinute: 1020 })">{{ t('inboxSla.addShift') }}</button>
        </template>
        <div class="flex flex-wrap gap-2">
          <button type="submit" :disabled="busy">{{ t('common.save') }}</button>
          <button v-if="existing" type="button" class="outline" :disabled="busy" @click="save(true)">{{ t('inboxSla.removePolicy') }}</button>
        </div>
      </template>
    </form>
  </details>
</template>

<style scoped>
select, input:not([type="checkbox"]) { @apply block w-full rounded-md border border-input bg-background px-2 py-1; }
</style>
