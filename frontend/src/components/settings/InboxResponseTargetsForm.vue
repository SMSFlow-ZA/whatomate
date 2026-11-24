<script setup lang="ts">
import { computed, nextTick, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Plus, Trash2 } from 'lucide-vue-next'
import type { InboxSLAPolicy } from '@/services/inbox'
import {
  inboxCalendarIssues,
  inboxMinuteTime,
  inboxTimeMinute,
} from '@/lib/inboxCalendar'

const policy = defineModel<InboxSLAPolicy>({ required: true })
defineProps<{ disabled?: boolean }>()
const { t, locale } = useI18n()
const id = useId()
const root = ref<HTMLElement>()
const issues = computed(() => inboxCalendarIssues(policy.value))
const weekdays = computed(() =>
  Array.from({ length: 7 }, (_, day) =>
    new Intl.DateTimeFormat(locale.value, {
      weekday: 'long',
      timeZone: 'UTC',
    }).format(new Date(Date.UTC(2023, 0, 1 + day))),
  ),
)
const timezones = [
  'UTC',
  ...((
    Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }
  ).supportedValuesOf?.('timeZone') ?? ['Africa/Johannesburg']),
]
const durationValid = (value: number) =>
  Number.isInteger(value) && value >= 1 && value <= 43200
function addDate() {
  policy.value.date_exceptions ??= []
  policy.value.date_exceptions.push({ date: '', intervals: [] })
  nextTick(() =>
    root.value
      ?.querySelectorAll<HTMLInputElement>('[data-exception-date]')
      .item((policy.value.date_exceptions?.length ?? 1) - 1)
      ?.focus(),
  )
}
function addHours(index: number) {
  const date = policy.value.date_exceptions?.[index]
  if (!date) return
  date.intervals ??= []
  date.intervals.push({ start_minute: 540, end_minute: 1020 })
}
async function removeDate(index: number) {
  policy.value.date_exceptions?.splice(index, 1)
  await nextTick()
  root.value?.querySelector<HTMLButtonElement>('[data-add-date]')?.focus()
}
async function removeShift(index: number) {
  policy.value.shifts.splice(index, 1)
  await nextTick()
  root.value?.querySelector<HTMLButtonElement>('[data-add-shift]')?.focus()
}
async function removeHours(dateIndex: number, index: number) {
  policy.value.date_exceptions?.[dateIndex]?.intervals?.splice(index, 1)
  await nextTick()
  root.value
    ?.querySelector<HTMLButtonElement>('[data-add-hours="' + dateIndex + '"]')
    ?.focus()
}
</script>

<template>
  <fieldset
    ref="root"
    class="inbox-response-targets min-w-0 border-0 p-0"
    :disabled="disabled"
  >
    <section class="inbox-form-section">
      <h3 class="inbox-section-heading">{{ t('inboxUX.responseTargets') }}</h3>
      <p class="inbox-caption">{{ t('inboxUX.targetMinutesHelp') }}</p>
      <div class="inbox-sla-targets">
        <label
          v-for="kind in ['first_reply', 'next_reply', 'resolution'] as const"
          :key="kind"
          class="inbox-label"
        >
          {{ t('inboxSla.target.' + kind) }}
          <input
            v-model.number="policy[`${kind}_minutes`]"
            type="number"
            min="1"
            max="43200"
            required
            :aria-invalid="!durationValid(policy[`${kind}_minutes`])"
            :aria-label="
              t('inboxSla.target.' + kind) + ' (' + t('inboxSla.minutes') + ')'
            "
          />
        </label>
      </div>
    </section>
    <section class="inbox-form-section">
      <h3 class="inbox-section-heading">{{ t('inboxUX.workingClock') }}</h3>
      <p class="inbox-caption">{{ t('inboxUX.slaClockHelp') }}</p>
      <label class="inbox-check mt-3"
        ><input v-model="policy.business_hours" type="checkbox" /><span
          >{{ t('inboxSla.businessHours')
          }}<span class="inbox-caption block">{{
            t('inboxUX.elapsedHelp')
          }}</span></span
        ></label
      >
      <template v-if="policy.business_hours">
        <label class="inbox-label mt-4 max-w-md"
          >{{ t('inboxSla.timezone')
          }}<input
            v-model.trim="policy.timezone"
            :list="id + '-zones'"
            required
            :aria-describedby="id + '-calendar-help'"
        /></label>
        <datalist :id="id + '-zones'">
          <option v-for="zone in timezones" :key="zone" :value="zone" />
        </datalist>
        <div class="mt-4 grid gap-3">
          <fieldset
            v-for="(shift, i) in policy.shifts"
            :key="i"
            class="inbox-shift-row"
          >
            <legend class="sr-only">
              {{ weekdays[shift.Day] }}
              {{ inboxMinuteTime(shift.StartMinute) }}–{{
                inboxMinuteTime(shift.EndMinute)
              }}
            </legend>
            <label class="inbox-label"
              >{{ t('inboxSla.day')
              }}<select v-model.number="shift.Day">
                <option v-for="(day, n) in weekdays" :key="day" :value="n">
                  {{ day }}
                </option>
              </select></label
            >
            <label class="inbox-label"
              >{{ t('inboxSla.from')
              }}<input
                type="time"
                :value="inboxMinuteTime(shift.StartMinute)"
                required
                @input="shift.StartMinute = inboxTimeMinute($event)"
            /></label>
            <label class="inbox-label"
              >{{ t('inboxSla.to')
              }}<input
                v-if="shift.EndMinute !== 1440"
                type="time"
                :value="inboxMinuteTime(shift.EndMinute)"
                required
                @input="shift.EndMinute = inboxTimeMinute($event)"
              /><span v-else class="inbox-control">24:00</span></label
            >
            <label class="inbox-check self-end pb-3"
              ><input
                type="checkbox"
                :checked="shift.EndMinute === 1440"
                @change="
                  shift.EndMinute = shift.EndMinute === 1440 ? 1020 : 1440
                "
              />{{ t('inboxSla.midnight') }}</label
            >
            <Button
              type="button"
              variant="ghost"
              size="sm"
              :aria-label="
                t('inboxReview.removeShift', {
                  day: weekdays[shift.Day],
                  from: inboxMinuteTime(shift.StartMinute),
                  to: inboxMinuteTime(shift.EndMinute),
                })
              "
              @click="removeShift(i)"
              ><Trash2 class="mr-2 h-4 w-4" aria-hidden="true" />{{
                t('common.remove')
              }}</Button
            >
          </fieldset>
        </div>
        <Button
          data-add-shift
          class="mt-3"
          type="button"
          variant="outline"
          size="sm"
          :disabled="policy.shifts.length >= 28"
          @click="
            policy.shifts.push({ Day: 1, StartMinute: 480, EndMinute: 1020 })
          "
          ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxSla.addShift')
          }}</Button
        >
      </template>
    </section>
    <section
      v-if="policy.business_hours || policy.date_exceptions?.length"
      class="inbox-form-section"
    >
      <h3 class="inbox-section-heading">{{ t('inboxCalendar.title') }}</h3>
      <p :id="id + '-calendar-help'" class="inbox-caption">
        {{ t('inboxCalendar.help', { zone: policy.timezone || 'UTC' }) }}
      </p>
      <p v-if="!policy.business_hours" class="inbox-notice mt-3" role="alert">
        {{ t('inboxCalendar.error.elapsedExceptions')
        }}<Button
          type="button"
          variant="outline"
          @click="policy.date_exceptions = []"
          >{{ t('inboxCalendar.removeAll') }}</Button
        >
      </p>
      <p v-else-if="!policy.date_exceptions?.length" class="inbox-caption mt-3">
        {{ t('inboxCalendar.empty') }}
      </p>
      <fieldset
        v-for="(date, i) in policy.date_exceptions"
        :key="i"
        class="min-w-0 border-b border-border py-4"
      >
        <legend class="sr-only">
          {{ t('inboxCalendar.exceptionNumber', { number: i + 1 }) }}
        </legend>
        <div class="flex flex-wrap items-end gap-3">
          <label class="inbox-label"
            >{{ t('inboxCalendar.date')
            }}<input
              v-model="date.date"
              data-exception-date
              type="date"
              min="0001-01-01"
              max="9999-12-31"
              required
              :aria-invalid="
                issues.some(
                  (issue) =>
                    issue.exception === i &&
                    ['date', 'duplicateDate'].includes(issue.code),
                )
              "
              :aria-label="
                t('inboxCalendar.date') +
                ' · ' +
                t('inboxCalendar.exceptionNumber', { number: i + 1 })
              "
          /></label>
          <Button
            type="button"
            variant="ghost"
            :aria-label="
              t('inboxCalendar.removeDate', { date: date.date || i + 1 })
            "
            @click="removeDate(i)"
            ><Trash2 class="mr-2 h-4 w-4" aria-hidden="true" />{{
              t('inboxCalendar.removeException')
            }}</Button
          >
        </div>
        <p v-if="!date.intervals?.length" class="inbox-caption mt-3">
          {{ t('inboxCalendar.closed') }}
        </p>
        <div
          v-for="(interval, n) in date.intervals"
          :key="n"
          class="mt-3 flex flex-wrap items-end gap-3"
        >
          <label class="inbox-label"
            >{{ t('inboxSla.from')
            }}<input
              type="time"
              :value="inboxMinuteTime(interval.start_minute)"
              required
              @input="interval.start_minute = inboxTimeMinute($event)"
          /></label>
          <label class="inbox-label"
            >{{ t('inboxSla.to')
            }}<input
              v-if="interval.end_minute !== 1440"
              type="time"
              :value="inboxMinuteTime(interval.end_minute)"
              required
              @input="interval.end_minute = inboxTimeMinute($event)"
            /><span v-else class="inbox-control">24:00</span></label
          >
          <label class="inbox-check self-end pb-3"
            ><input
              type="checkbox"
              :checked="interval.end_minute === 1440"
              @change="
                interval.end_minute = interval.end_minute === 1440 ? 1020 : 1440
              "
            />{{ t('inboxSla.midnight') }}</label
          >
          <Button
            type="button"
            variant="ghost"
            :aria-label="
              t('inboxCalendar.removeHours', {
                number: n + 1,
                date: date.date || i + 1,
              })
            "
            @click="removeHours(i, n)"
            >{{ t('common.remove') }}</Button
          >
        </div>
        <Button
          class="mt-3"
          :data-add-hours="i"
          type="button"
          variant="outline"
          :disabled="
            !policy.business_hours || (date.intervals?.length ?? 0) >= 28
          "
          @click="addHours(i)"
          >{{ t('inboxCalendar.addHours') }}</Button
        >
      </fieldset>
      <Button
        class="mt-3"
        data-add-date
        type="button"
        variant="outline"
        :disabled="
          !policy.business_hours || (policy.date_exceptions?.length ?? 0) >= 366
        "
        @click="addDate"
        ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
          t('inboxCalendar.addDate')
        }}</Button
      >
    </section>
    <ul
      v-if="issues.length"
      class="grid gap-2 py-3 text-sm text-destructive"
      aria-live="polite"
    >
      <li v-for="(issue, index) in issues" :key="index">
        {{
          t('inboxCalendar.error.' + issue.code, {
            number: (issue.exception ?? 0) + 1,
          })
        }}
      </li>
    </ul>
  </fieldset>
</template>
