<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { isAxiosError } from 'axios'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { ArrowLeft, Plus, Search, Trash2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useAuthStore } from '@/stores/auth'
import { inboxService, type FieldDefinition } from '@/services/inbox'
import {
  inboxIntake,
  type IntakeMapping,
  type IntakeMappingInput,
} from '@/services/inboxIntake'

const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const allowed = computed(
  () =>
    ['settings.general', 'flows.chatbot', 'chat', 'contacts'].every(
      (resource) => auth.hasPermission(resource, 'write'),
    ) && auth.hasPermission('contacts', 'read'),
)
const items = ref<IntakeMapping[]>([]),
  definitions = ref<FieldDefinition[]>([])
const loading = ref(false),
  failed = ref(false),
  busy = ref(false),
  editing = ref(false),
  conflict = ref(false),
  more = ref(false)
const selected = ref(''),
  query = ref(''),
  error = ref(''),
  baseline = ref('')
const heading = ref<HTMLElement>()
let generation = 0,
  mutation = 0,
  originID = 'intake-new-mapping'
const defaults = (): IntakeMappingInput => ({
  revision: 0,
  name: '',
  description: '',
  consent_key: 'consent',
  enabled: false,
  fields: [],
})
const form = ref(defaults())
const visible = computed(() =>
  items.value.filter((item) =>
    item.name.toLocaleLowerCase().includes(query.value.toLocaleLowerCase()),
  ),
)
const keyValid = (key: string) =>
  /^[a-z][a-z0-9_]{0,63}$/.test(key) && key !== 'flow_token'
const valid = computed(() => {
  const value = form.value
  return (
    !!value.name.trim() &&
    keyValid(value.consent_key) &&
    value.fields.length > 0 &&
    value.fields.length <= 20 &&
    new Set(value.fields.map((field) => field.input)).size ===
      value.fields.length &&
    new Set(value.fields.map((field) => field.definition_id)).size ===
      value.fields.length &&
    value.fields.every(
      (field) =>
        keyValid(field.input) &&
        definitions.value.some(
          (d) => d.id === field.definition_id && !d.archived_at,
        ),
    )
  )
})
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(
    () => editing.value && JSON.stringify(form.value) !== baseline.value,
  )
function confirmLeave(action: () => void) {
  if (!busy.value) requestDiscard(action)
}
defineExpose({ confirmLeave })
async function load() {
  const request = ++generation,
    context = viewer.value
  loading.value = true
  failed.value = false
  try {
    const [reply, fields] = await Promise.all([
      inboxIntake.list(),
      inboxService.fieldDefinitions(),
    ])
    if (request !== generation || context !== viewer.value) return
    items.value = reply.data.data.mappings
    more.value = reply.data.data.has_more
    definitions.value = fields.data.data.definitions
  } catch {
    if (request === generation && context === viewer.value) {
      failed.value = true
      items.value = []
      definitions.value = []
    }
  } finally {
    if (request === generation && context === viewer.value)
      loading.value = false
  }
}
function begin(item?: IntakeMapping) {
  if (busy.value) return
  originID = item ? 'intake-edit-' + item.id : 'intake-new-mapping'
  confirmLeave(async () => {
    selected.value = item?.id ?? ''
    form.value = item
      ? {
          revision: item.revision,
          name: item.name,
          description: item.description,
          consent_key: item.consent_key,
          enabled: item.enabled,
          fields: item.fields.map((field) => ({ ...field })),
        }
      : defaults()
    baseline.value = JSON.stringify(form.value)
    editing.value = true
    error.value = ''
    conflict.value = false
    await nextTick()
    heading.value?.focus()
  })
}
function close() {
  confirmLeave(async () => {
    editing.value = false
    await nextTick()
    restoreListFocus()
  })
}
function restoreListFocus() {
  // The list is remounted after editing; its previous DOM nodes are detached.
  // Reacquire the logical trigger, falling back when a filter/permission change
  // has removed that row. Disabled buttons cannot receive focus.
  const trigger = document.getElementById(originID)
  const create = document.getElementById('intake-new-mapping')
  if (trigger instanceof HTMLButtonElement && !trigger.disabled) trigger.focus()
  else if (create instanceof HTMLButtonElement && !create.disabled)
    create.focus()
  else heading.value?.focus()
}
async function save() {
  if (busy.value || !allowed.value || !valid.value || conflict.value) return
  const request = ++mutation,
    context = viewer.value
  busy.value = true
  error.value = ''
  let restoreFocus = false
  const input = JSON.parse(JSON.stringify(form.value)) as IntakeMappingInput
  try {
    if (selected.value) await inboxIntake.update(selected.value, input)
    else await inboxIntake.create({ ...input, enabled: false })
    if (request !== mutation || context !== viewer.value) return
    editing.value = false
    await load()
    if (request !== mutation || context !== viewer.value) return
    toast.success(t('settings.saved'))
    restoreFocus = true
  } catch (problem) {
    if (request !== mutation || context !== viewer.value) return
    conflict.value = isAxiosError(problem) && problem.response?.status === 409
    error.value = t(
      conflict.value
        ? 'inboxIntake.mappingConflict'
        : 'inboxIntake.mappingSaveFailed',
    )
  } finally {
    if (request === mutation && context === viewer.value) {
      busy.value = false
      if (restoreFocus) {
        await nextTick()
        restoreListFocus()
      }
    }
  }
}
function reloadLatest() {
  confirmLeave(async () => {
    const id = selected.value
    await load()
    if (failed.value) return
    const item = items.value.find((item) => item.id === id)
    // The user has explicitly accepted discarding the stale draft.
    baseline.value = JSON.stringify(form.value)
    if (item) begin(item)
    else editing.value = false
  })
}
async function disable(item: IntakeMapping) {
  if (busy.value || !allowed.value) return
  const request = ++mutation,
    context = viewer.value
  busy.value = true
  error.value = ''
  try {
    await inboxIntake.disable(item.id, item.revision)
    if (request !== mutation || context !== viewer.value) return
    await load()
  } catch (problem) {
    if (request !== mutation || context !== viewer.value) return
    error.value = t(
      isAxiosError(problem) && problem.response?.status === 409
        ? 'inboxIntake.mappingConflict'
        : 'inboxIntake.mappingSaveFailed',
    )
  } finally {
    if (request === mutation && context === viewer.value) busy.value = false
  }
}
watch(
  viewer,
  () => {
    generation++
    mutation++
    cancelDiscard()
    editing.value = false
    busy.value = false
    items.value = []
    definitions.value = []
    form.value = defaults()
    if (allowed.value) void load()
  },
  { immediate: true },
)
onUnmounted(() => {
  generation++
  mutation++
})
</script>
<template>
  <UnsavedChangesDialog
    :open="discardOpen"
    @leave="confirmDiscard"
    @stay="cancelDiscard"
  />
  <section v-if="allowed" aria-labelledby="intake-mappings-heading">
    <template v-if="!editing">
      <div class="inbox-toolbar">
        <div>
          <h2
            id="intake-mappings-heading"
            ref="heading"
            tabindex="-1"
            class="font-semibold"
          >
            {{ t('inboxIntake.mappings') }}
          </h2>
          <p class="inbox-caption mt-1">{{ t('inboxIntake.mappingsHelp') }}</p>
        </div>
        <Button
          id="intake-new-mapping"
          :disabled="loading || failed || more || busy"
          @click="begin()"
          ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxIntake.newMapping')
          }}</Button
        >
      </div>
      <div v-if="error" role="alert" class="inbox-notice mb-4">
        {{ error
        }}<Button variant="outline" :disabled="busy" @click="load">{{
          t('common.refresh')
        }}</Button>
      </div>
      <p v-if="loading" role="status" class="inbox-caption">
        {{ t('common.loading') }}
      </p>
      <div v-else-if="failed" role="alert" class="inbox-notice">
        {{ t('inboxIntake.mappingLoadFailed')
        }}<Button variant="outline" @click="load">{{
          t('common.retry')
        }}</Button>
      </div>
      <template v-else>
        <p v-if="more" role="alert" class="inbox-notice">
          {{ t('inboxIntake.incompleteMappings') }}
        </p>
        <label class="inbox-search mb-4"
          ><Search class="h-4 w-4" aria-hidden="true" /><input
            v-model="query"
            class="placeholder:text-muted-foreground"
            :aria-label="t('inboxIntake.searchMappings')"
            :placeholder="t('inboxIntake.searchMappings')"
        /></label>
        <div class="inbox-panel">
          <ul v-if="visible.length" class="inbox-list">
            <li v-for="item in visible" :key="item.id" class="inbox-list-row">
              <div class="inbox-list-copy">
                <p class="inbox-list-title">{{ item.name }}</p>
                <p class="inbox-caption mt-1">
                  {{
                    item.description ||
                    t('inboxIntake.mappingSummary', {
                      count: item.fields.length,
                    })
                  }}
                </p>
                <p
                  v-if="item.unavailable_fields?.length"
                  class="mt-1 text-sm text-destructive"
                >
                  {{ t('inboxIntake.mappingFieldsUnavailable') }}
                </p>
              </div>
              <span class="inbox-badge">{{
                t(item.enabled ? 'inboxIntake.enabled' : 'inboxIntake.disabled')
              }}</span>
              <Button
                v-if="item.enabled"
                variant="outline"
                size="sm"
                :disabled="busy"
                :aria-label="t('inboxIntake.disableNamed', { name: item.name })"
                @click="disable(item)"
                >{{ t('inboxIntake.disable') }}</Button
              >
              <Button
                :id="'intake-edit-' + item.id"
                variant="outline"
                size="sm"
                :disabled="busy"
                @click="begin(item)"
                >{{ t('common.edit') }}</Button
              >
            </li>
          </ul>
          <div v-else class="inbox-empty">
            <h3>
              {{ t(query ? 'inboxUX.noMatches' : 'inboxIntake.noMappings') }}
            </h3>
            <p>{{ t('inboxIntake.mappingsHelp') }}</p>
            <Button v-if="!query" variant="outline" @click="begin()">{{
              t('inboxIntake.newMapping')
            }}</Button>
          </div>
        </div>
      </template>
    </template>
    <template v-else>
      <Button
        variant="ghost"
        size="sm"
        class="mb-4 -ml-2"
        :disabled="busy"
        @click="close"
        ><ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
          t('inboxIntake.backToMappings')
        }}</Button
      >
      <form class="inbox-form" :aria-busy="busy" @submit.prevent="save">
        <div v-if="error" role="alert" class="inbox-notice">
          {{ error
          }}<Button
            v-if="conflict"
            type="button"
            variant="outline"
            @click="reloadLatest"
            >{{ t('inboxIntake.reload') }}</Button
          >
        </div>
        <fieldset :disabled="busy" class="contents">
          <section class="inbox-form-section">
            <h2 id="intake-mappings-heading" ref="heading" tabindex="-1">
              {{
                t(
                  selected
                    ? 'inboxIntake.editMapping'
                    : 'inboxIntake.newMapping',
                )
              }}
            </h2>
            <p>{{ t('inboxIntake.mappingsHelp') }}</p>
            <label class="inbox-label"
              >{{ t('common.name')
              }}<input v-model.trim="form.name" required maxlength="100"
            /></label>
            <label class="inbox-label mt-4"
              >{{ t('common.description')
              }}<textarea
                v-model="form.description"
                rows="2"
                maxlength="1000"
              />
            </label>
            <label class="inbox-label mt-4"
              >{{ t('inboxIntake.consentKey')
              }}<input
                v-model="form.consent_key"
                required
                pattern="[a-z][a-z0-9_]{0,63}"
                aria-describedby="intake-consent-help"
              /><span id="intake-consent-help" class="inbox-caption">{{
                t('inboxIntake.consentHelp')
              }}</span></label
            >
          </section>
          <section class="inbox-form-section">
            <h2>{{ t('inboxIntake.mapAnswers') }}</h2>
            <p>{{ t('inboxIntake.mapAnswersHelp') }}</p>
            <div
              v-for="(field, index) in form.fields"
              :key="index"
              class="grid min-w-0 gap-3 border-b border-border py-4 last:border-0"
            >
              <div class="inbox-grid">
                <label class="inbox-label"
                  >{{ t('inboxIntake.answerName', { number: index + 1 })
                  }}<input
                    v-model="field.input"
                    required
                    pattern="[a-z][a-z0-9_]{0,63}"
                    maxlength="64"
                /></label>
                <label class="inbox-label"
                  >{{ t('inboxIntake.destinationField', { number: index + 1 })
                  }}<select v-model="field.definition_id" required>
                    <option value="">{{ t('inboxFields.choose') }}</option>
                    <option
                      v-if="
                        field.definition_id &&
                        !definitions.some(
                          (d) => d.id === field.definition_id && !d.archived_at,
                        )
                      "
                      :value="field.definition_id"
                      disabled
                    >
                      {{ t('inboxFields.unavailable') }}
                    </option>
                    <option
                      v-for="d in definitions.filter((d) => !d.archived_at)"
                      :key="d.id"
                      :value="d.id"
                    >
                      {{ d.label }} · {{ t('inboxFields.scope.' + d.scope) }} ·
                      {{ t('inboxFields.kind.' + d.kind) }}
                    </option>
                  </select></label
                >
              </div>
              <div class="flex flex-wrap items-center justify-between gap-2">
                <label class="inbox-check"
                  ><input v-model="field.required" type="checkbox" />{{
                    t('inboxIntake.answerRequired')
                  }}</label
                ><Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  :aria-label="
                    t('inboxIntake.removeAnswer', { number: index + 1 })
                  "
                  @click="form.fields.splice(index, 1)"
                  ><Trash2 class="mr-2 h-4 w-4" aria-hidden="true" />{{
                    t('common.remove')
                  }}</Button
                >
              </div>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              class="mt-4"
              :disabled="form.fields.length >= 20"
              @click="
                form.fields.push({
                  input: '',
                  definition_id: '',
                  required: false,
                })
              "
              ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
                t('inboxIntake.addAnswer')
              }}</Button
            >
            <p v-if="!valid" role="status" class="inbox-caption mt-3">
              {{ t('inboxIntake.mappingValidation') }}
            </p>
          </section>
          <section class="inbox-form-section">
            <h2>{{ t('inboxIntake.mappingAvailability') }}</h2>
            <label v-if="selected" class="inbox-check"
              ><input v-model="form.enabled" type="checkbox" />{{
                t('inboxIntake.enableMapping')
              }}</label
            >
            <p>
              {{
                t(
                  selected
                    ? 'inboxIntake.editEffect'
                    : 'inboxIntake.createdDisabled',
                )
              }}
            </p>
          </section>
          <footer class="inbox-form-footer">
            <Button type="submit" :disabled="!valid || conflict">{{
              t('common.save')
            }}</Button
            ><Button type="button" variant="outline" @click="close">{{
              t('common.cancel')
            }}</Button>
          </footer>
        </fieldset>
      </form>
    </template>
  </section>
</template>
