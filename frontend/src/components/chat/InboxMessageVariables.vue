<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Braces } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { inboxService, type InboxMessageVariable } from '@/services/inbox'
import { useAuthStore } from '@/stores/auth'

defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ insert: [token: string] }>()
const { t } = useI18n()
const auth = useAuthStore()
const open = ref(false),
  busy = ref(false),
  failed = ref(false)
const query = ref(''),
  selected = ref('')
const variables = ref<InboxMessageVariable[]>([])
const search = ref<HTMLInputElement>()
const results = ref<HTMLSelectElement>()
const selectedToken = computed(() => '{{' + selected.value + '}}')
let generation = 0
let inserted = false
const visible = computed(() =>
  variables.value.filter(
    (variable) =>
      !variable.archived &&
      `${variable.label} ${variable.key}`
        .toLocaleLowerCase()
        .includes(query.value.toLocaleLowerCase()),
  ),
)
watch(visible, (options) => {
  if (!options.some((option) => option.key === selected.value))
    selected.value = options[0]?.key ?? ''
})
async function load() {
  const current = ++generation,
    organization = auth.organizationId
  // Keep focus inside the popover while retry/loading controls are replaced.
  search.value?.focus()
  busy.value = true
  failed.value = false
  variables.value = []
  try {
    const { data } = await inboxService.messageVariables()
    if (
      current !== generation ||
      organization !== auth.organizationId ||
      !open.value
    )
      return
    variables.value = data.data.variables
    await nextTick()
    if (
      current === generation &&
      organization === auth.organizationId &&
      open.value
    )
      search.value?.focus()
  } catch {
    if (current === generation) failed.value = true
  } finally {
    if (current === generation) busy.value = false
  }
}
watch(open, (value) => {
  generation++
  if (value) {
    inserted = false
    query.value = ''
    selected.value = ''
    void load()
  } else busy.value = false
})
watch(
  () => [auth.organizationId, auth.user?.id],
  () => {
    generation++
    open.value = false
    variables.value = []
  },
)
onUnmounted(() => {
  generation++
})
function insert() {
  if (
    !open.value ||
    busy.value ||
    !visible.value.some((variable) => variable.key === selected.value)
  )
    return
  const token = '{{' + selected.value + '}}'
  inserted = true
  open.value = false
  emit('insert', token)
}
function restoreFocus(event: Event) {
  // Insertion restores the authoring selection, rather than the toolbar button.
  if (inserted) event.preventDefault()
  inserted = false
}
</script>

<template>
  <Popover v-model:open="open">
    <PopoverTrigger as-child>
      <Button
        type="button"
        variant="outline"
        size="sm"
        :disabled="disabled"
        class="min-h-[44px] w-fit justify-self-start"
      >
        <Braces class="mr-2 h-4 w-4" aria-hidden="true" />{{
          t('inboxPersonalisation.insert')
        }}
      </Button>
    </PopoverTrigger>
    <PopoverContent
      align="start"
      class="inbox-ui grid w-80 max-w-[calc(100vw-2rem)] gap-3"
      @close-auto-focus="restoreFocus"
    >
      <p class="text-sm font-medium">{{ t('inboxPersonalisation.choose') }}</p>
      <label class="inbox-label"
        >{{ t('inboxPersonalisation.search') }}
        <input
          ref="search"
          style="font-size: 16px"
          v-model="query"
          type="search"
          :readonly="busy"
          @keydown.down.prevent="results?.focus()"
        />
      </label>
      <p v-if="busy" role="status" class="inbox-caption">
        {{ t('common.loading') }}
      </p>
      <template v-else-if="failed">
        <p role="alert" class="inbox-caption">
          {{ t('inboxPersonalisation.loadError') }}
        </p>
        <Button
          type="button"
          variant="outline"
          class="min-h-[44px]"
          @click="load"
          >{{ t('common.retry') }}</Button
        >
      </template>
      <template v-else>
        <label v-if="visible.length" class="inbox-label"
          >{{ t('inboxPersonalisation.available') }}
          <select
            ref="results"
            style="font-size: 16px"
            :aria-label="t('inboxPersonalisation.available')"
            v-model="selected"
            size="6"
            class="min-h-40"
            @keydown.enter.prevent="insert"
          >
            <option
              v-for="variable in visible"
              :key="variable.key"
              :value="variable.key"
            >
              {{ variable.label }} · {{ variable.scope }}
            </option>
          </select>
        </label>
        <p v-else role="status" class="inbox-caption">
          {{ t('inboxPersonalisation.noResults') }}
        </p>
        <p v-if="selected" class="inbox-caption break-all">
          {{ selectedToken }}
        </p>
        <Button
          type="button"
          class="min-h-[44px]"
          :disabled="!selected"
          @click="insert"
          >{{ t('inboxPersonalisation.insert') }}</Button
        >
      </template>
    </PopoverContent>
  </Popover>
</template>
