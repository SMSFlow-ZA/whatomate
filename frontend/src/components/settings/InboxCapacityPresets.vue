<script setup lang="ts">
import { useInboxDialogFocus } from '@/composables/useInboxDialogFocus'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
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
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import {
  inboxService,
  type CapacityPreset,
  type CapacityChange,
} from '@/services/inbox'
const props = defineProps<{
  team: string
  account: string
  agents: string[]
}>()
const emit = defineEmits<{ applied: [] }>()
const { t } = useI18n(),
  presets = ref<CapacityPreset[]>([]),
  selected = ref(''),
  name = ref(''),
  maximum = ref(0),
  editing = ref(false),
  busy = ref(false),
  error = ref(''),
  reviewing = ref(false),
  changes = ref<CapacityChange[]>([]),
  confirmation = ref('')
const baseline = ref('')
const removing = ref(false)
const draft = () => JSON.stringify([name.value, maximum.value])
const dirty = computed(() => editing.value && draft() !== baseline.value)
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(() => dirty.value)
defineExpose({ confirmLeave: requestDiscard })
let generation = 0
watch(
  [() => props.team, () => props.account, () => props.agents.join(',')],
  () => {
    generation++
    reviewing.value = false
    confirmation.value = ''
  },
)
async function load() {
  try {
    presets.value = (await inboxService.capacityPresets()).data.data.presets
    error.value = ''
  } catch {
    error.value = t('inboxQuality.presetLoadError')
  }
}
function edit() {
  const preset = presets.value.find((item) => item.id === selected.value)
  name.value = preset?.name ?? ''
  maximum.value = preset?.max_active ?? 0
  editing.value = true
  baseline.value = draft()
}
async function save() {
  busy.value = true
  try {
    const response = await inboxService.saveCapacityPreset(
      { name: name.value.trim(), max_active: maximum.value },
      selected.value || undefined,
    )
    selected.value = response.data.data.preset.id
    editing.value = false
    await load()
  } catch {
    error.value = t('inboxCapacity.saveError')
  } finally {
    busy.value = false
  }
}
async function preview() {
  if (!selected.value || busy.value) return
  const request = ++generation
  busy.value = true
  error.value = ''
  try {
    const { data } = await inboxService.previewCapacityPreset(selected.value, {
      team_id: props.team,
      account_id: props.account,
      agent_ids: [...props.agents],
    })
    if (request !== generation) return
    changes.value = data.data.changes
    confirmation.value = data.data.confirmation
    reviewing.value = true
  } catch {
    if (request === generation) error.value = t('inboxCapacity.loadError')
  } finally {
    busy.value = false
  }
}
async function apply() {
  const request = generation
  busy.value = true
  try {
    await inboxService.applyCapacityPreset(selected.value, {
      team_id: props.team,
      account_id: props.account,
      agent_ids: [...props.agents],
      confirmation: confirmation.value,
    })
    if (request !== generation) return
    reviewing.value = false
    emit('applied')
  } catch {
    error.value = t('inboxQuality.presetChanged')
    reviewing.value = false
  } finally {
    busy.value = false
  }
}
onMounted(load)

const restoreDialogFocus = useInboxDialogFocus(reviewing)
function changePreset(event: Event) {
  const control = event.target as HTMLSelectElement
  const next = control.value
  control.value = selected.value
  requestDiscard(() => {
    selected.value = next
    editing.value = false
    generation++
    reviewing.value = false
  })
}
async function remove() {
  if (!selected.value || busy.value) return
  busy.value = true
  try {
    await inboxService.deleteCapacityPreset(selected.value)
    selected.value = ''
    editing.value = false
    removing.value = false
    generation++
    await load()
  } catch {
    error.value = t('inboxReview.presetDeleteError')
  } finally {
    busy.value = false
  }
}
</script>
<template>
  <UnsavedChangesDialog
    :open="discardOpen"
    @leave="confirmDiscard"
    @stay="cancelDiscard"
  />
  <AlertDialog
    :open="removing"
    @update:open="
      (value) => {
        if (!busy) removing = value
      }
    "
    ><AlertDialogContent
      ><AlertDialogHeader
        ><AlertDialogTitle>{{ t('inboxReview.removePreset') }}</AlertDialogTitle
        ><AlertDialogDescription>{{
          t('inboxReview.removePresetHelp')
        }}</AlertDialogDescription></AlertDialogHeader
      ><AlertDialogFooter
        ><AlertDialogCancel :disabled="busy">{{
          t('common.cancel')
        }}</AlertDialogCancel
        ><AlertDialogAction :disabled="busy" @click.prevent="remove">{{
          t('common.remove')
        }}</AlertDialogAction></AlertDialogFooter
      ></AlertDialogContent
    ></AlertDialog
  >
  <details class="inbox-subsection">
    <summary class="min-h-10 cursor-pointer py-2 font-medium">
      {{ t('inboxQuality.capacityPresets') }}
    </summary>
    <p class="inbox-caption my-3">{{ t('inboxQuality.presetHelp') }}</p>
    <p v-if="error" role="alert" class="inbox-notice">
      {{ error }}
      <Button size="sm" variant="outline" @click="load">{{
        t('common.retry')
      }}</Button>
    </p>
    <div class="flex flex-wrap items-end gap-2">
      <label class="inbox-label grow"
        >{{ t('inboxQuality.capacityPresets')
        }}<select :value="selected" :disabled="busy" @change="changePreset">
          <option value="">{{ t('inboxQuality.newPreset') }}</option>
          <option v-for="preset in presets" :key="preset.id" :value="preset.id">
            {{ preset.name }} ·
            {{ preset.max_active || t('inboxCapacity.unlimited') }}
          </option>
        </select></label
      ><Button
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="requestDiscard(edit)"
        >{{ t(selected ? 'common.edit' : 'common.add') }}</Button
      ><Button
        v-if="selected"
        size="sm"
        variant="outline"
        :disabled="busy"
        @click="
          requestDiscard(() => {
            editing = false
            removing = true
          })
        "
        >{{ t('common.remove') }}</Button
      ><Button
        size="sm"
        :disabled="!selected || busy || !agents.length"
        @click="preview"
        >{{ t('inboxQuality.previewApply') }}</Button
      >
    </div>
    <form v-if="editing" class="mt-4 grid gap-3" @submit.prevent="save">
      <label class="inbox-label"
        >{{ t('inboxQuality.presetName')
        }}<input v-model="name" required maxlength="100" /></label
      ><label class="inbox-label"
        >{{ t('inboxUX.capacityLimit')
        }}<input
          v-model.number="maximum"
          type="number"
          min="0"
          max="10000"
          required
      /></label>
      <div class="flex gap-2">
        <Button size="sm" :disabled="busy">{{ t('common.save') }}</Button
        ><Button
          type="button"
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="
            requestDiscard(() => {
              editing = false
            })
          "
          >{{ t('common.cancel') }}</Button
        >
      </div>
    </form>
  </details>
  <Dialog
    :open="reviewing"
    @update:open="
      (value) => {
        if (!busy) reviewing = value
      }
    "
    ><DialogContent
      @close-auto-focus="restoreDialogFocus"
      class="inbox-ui max-h-[85dvh] overflow-auto"
      ><DialogHeader
        ><DialogTitle>{{ t('inboxQuality.previewApply') }}</DialogTitle
        ><DialogDescription>{{
          t('inboxQuality.presetHelp')
        }}</DialogDescription></DialogHeader
      >
      <ul class="grid gap-2">
        <li
          v-for="change in changes"
          :key="change.agent_id"
          class="flex justify-between gap-3"
        >
          <span>{{ change.name }}</span
          ><span class="tabular-nums"
            >{{ change.before || t('inboxCapacity.unlimited') }} →
            {{ change.after || t('inboxCapacity.unlimited') }}</span
          >
        </li>
      </ul>
      <DialogFooter
        ><Button
          variant="outline"
          :disabled="busy"
          @click="reviewing = false"
          >{{ t('common.cancel') }}</Button
        ><Button :disabled="busy" @click="apply">{{
          t('inboxQueue.apply')
        }}</Button></DialogFooter
      ></DialogContent
    ></Dialog
  >
</template>
